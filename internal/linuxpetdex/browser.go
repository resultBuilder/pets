package linuxpetdex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"codex-pets/internal/protocol"
)

var ErrUnsupported = errors.New("Linux Petdex browser requires linux, cgo, GTK, and WebKitGTK")

const (
	eventNativeReady        = "codex-pets-native-ready"
	eventBrowserPets        = "codex-pets-native-browser-pets"
	eventInstalledPets      = "codex-pets-native-installed-pets"
	eventImportResult       = "codex-pets-native-import-result"
	eventPiInstallResult    = "codex-pets-native-pi-install-result"
	eventPiUninstallResult  = "codex-pets-native-pi-uninstall-result"
	eventPiInstallStatus    = "codex-pets-native-pi-install-status"
	defaultRequestTimeout   = 5 * time.Second
	linuxPetdexSmokeFileEnv = "CODEX_PETS_GUI_SMOKE_FILE"
)

type Options struct {
	SocketPath     string
	StaticDir      string
	SmokeFile      string
	RequestTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	if o.SmokeFile == "" {
		o.SmokeFile = os.Getenv(linuxPetdexSmokeFileEnv)
	}
	return o
}

func Open(ctx context.Context, options Options) error {
	return openNative(ctx, options.withDefaults())
}

type browser struct {
	ctx     context.Context
	options Options
	client  *daemonClient
	assets  *assetServer
	smoke   *smokeRecorder

	eval func(string)

	mu             sync.RWMutex
	rows           []protocol.BrowserPetRow
	importInFlight bool
}

func newBrowser(ctx context.Context, options Options) (*browser, error) {
	options = options.withDefaults()
	b := &browser{
		ctx:     ctx,
		options: options,
		client:  newDaemonClient(options.SocketPath, options.RequestTimeout),
		smoke:   newSmokeRecorder(options.SmokeFile),
	}
	assets, err := newAssetServer(options.StaticDir, b.currentRows)
	if err != nil {
		return nil, err
	}
	b.assets = assets
	return b, nil
}

func (b *browser) close() {
	if b.assets != nil {
		b.assets.close()
	}
	if b.smoke != nil {
		b.smoke.close()
	}
}

func (b *browser) pageURL() string {
	return b.assets.pageURL("index.html")
}

func (b *browser) initScript() string {
	return `
(() => {
  const postMessage = (payload) => {
    window.webkit?.messageHandlers?.codexPets?.postMessage(JSON.stringify(payload || {}));
  };
  Object.defineProperty(window, "CodexPetsNative", {
    configurable: true,
    value: { isNativeShell: true, postMessage }
  });
  document.documentElement.classList.add("native-shell");
  const notifyReady = () => {
    window.dispatchEvent(new CustomEvent("codex-pets-native-ready"));
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", notifyReady, { once: true });
  } else {
    notifyReady();
  }
})();
`
}

func (b *browser) onLoaded() {
	b.dispatchEvent(eventNativeReady, map[string]any{})
	b.sendBrowserPets()
	b.sendInstalledPets()
	b.sendPiExtensionStatus()
	b.scheduleSmokeReport()
}

func (b *browser) handleMessage(raw string) {
	var message bridgeMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		return
	}
	if message.Action == "smokeReport" {
		b.recordSmokeReport(message)
		return
	}
	if !allowedBridgeAction(message.Action) {
		return
	}

	switch message.Action {
	case "listBrowserPets":
		b.sendBrowserPets()
	case "listInstalledPets":
		b.sendInstalledPets()
	case "importPet":
		b.importPet(message.PetID)
	case "selectInstalledPet":
		b.selectInstalledPet(message.PetID)
	case "installPiExtension":
		b.relayPiExtension(eventPiInstallResult, protocol.MethodPiExtensionInstall)
	case "uninstallPiExtension":
		b.relayPiExtension(eventPiUninstallResult, protocol.MethodPiExtensionUninstall)
	case "getPiExtensionStatus":
		b.sendPiExtensionStatus()
	}
}

type bridgeMessage struct {
	Action       string `json:"action"`
	PetID        string `json:"petId,omitempty"`
	Event        string `json:"event,omitempty"`
	BootStarted  bool   `json:"bootStarted,omitempty"`
	BootLoaded   bool   `json:"bootLoaded,omitempty"`
	BootError    string `json:"bootError,omitempty"`
	RowCount     int    `json:"rowCount,omitempty"`
	Status       string `json:"status,omitempty"`
	SelectedName string `json:"selectedName,omitempty"`
}

func allowedBridgeAction(action string) bool {
	switch action {
	case "importPet",
		"listBrowserPets",
		"listInstalledPets",
		"selectInstalledPet",
		"installPiExtension",
		"uninstallPiExtension",
		"getPiExtensionStatus":
		return true
	default:
		return false
	}
}

func (b *browser) sendBrowserPets() {
	var list protocol.BrowserPetList
	if err := b.request(protocol.MethodPetsBrowserList, map[string]any{}, &list); err != nil {
		return
	}
	b.mu.Lock()
	b.rows = append([]protocol.BrowserPetRow(nil), list.Rows...)
	b.mu.Unlock()

	pets := make([]map[string]any, 0, len(list.Rows))
	for _, row := range list.Rows {
		if payload := browserRowPayload(row, b.assets.assetURL); payload != nil {
			pets = append(pets, payload)
		}
	}
	b.dispatchEvent(eventBrowserPets, petListEventDetail(pets, b.selectedPetID()))
}

func (b *browser) sendInstalledPets() {
	var snapshot protocol.Snapshot
	if err := b.request(protocol.MethodSnapshotGet, map[string]any{}, &snapshot); err != nil {
		return
	}
	pets := make([]map[string]any, 0, len(snapshot.InstalledPets))
	for _, pet := range snapshot.InstalledPets {
		row := protocol.BrowserPetRow{PetRef: pet, Installed: true}
		if payload := browserRowPayload(row, b.assets.assetURL); payload != nil {
			pets = append(pets, payload)
		}
	}
	b.dispatchEvent(eventInstalledPets, petListEventDetail(pets, snapshot.SelectedPetID))
}

func (b *browser) selectedPetID() string {
	var snapshot protocol.Snapshot
	if err := b.request(protocol.MethodSnapshotGet, map[string]any{}, &snapshot); err != nil {
		return ""
	}
	return snapshot.SelectedPetID
}

func (b *browser) importPet(petID string) {
	if strings.TrimSpace(petID) == "" {
		b.dispatchImportResult(false, "Selected pet was not found in the catalog")
		return
	}
	if !b.beginImport() {
		b.dispatchImportResult(false, "Another import is already running")
		return
	}
	defer b.endImport()

	row, ok := b.rowByID(petID)
	if !ok {
		b.sendBrowserPets()
		row, ok = b.rowByID(petID)
	}
	if !ok || row.Path == "" {
		b.dispatchImportResult(false, "Selected pet was not found in the catalog")
		return
	}

	var result protocol.PetOpResult
	if err := b.request(protocol.MethodPetImport, protocol.PetImport{Directory: row.Path}, &result); err != nil {
		b.dispatchImportResult(false, err.Error())
		return
	}
	if result.OK && result.Pet != nil {
		_ = b.request(protocol.MethodPetSelect, protocol.PetSelect{PetID: result.Pet.ID}, nil)
	}
	message := result.Message
	if message == "" {
		if result.OK {
			message = "Selected " + firstNonEmpty(resultName(result.Pet), row.DisplayName)
		} else {
			message = "Import failed"
		}
	}
	b.dispatchImportResult(result.OK, message)
	b.sendBrowserPets()
	b.sendInstalledPets()
}

func (b *browser) selectInstalledPet(petID string) {
	row, ok := b.rowByID(petID)
	if !ok {
		b.sendBrowserPets()
		row, ok = b.rowByID(petID)
	}
	if !ok || !row.Installed {
		b.dispatchImportResult(false, "Installed pet was not found")
		return
	}
	if err := b.request(protocol.MethodPetSelect, protocol.PetSelect{PetID: row.ID}, nil); err != nil {
		b.dispatchImportResult(false, err.Error())
		return
	}
	b.dispatchImportResult(true, "Selected "+row.DisplayName)
	b.sendBrowserPets()
	b.sendInstalledPets()
}

func (b *browser) relayPiExtension(eventName string, method string) {
	var result protocol.PiExtensionResult
	if err := b.request(method, map[string]any{}, &result); err != nil {
		b.dispatchEvent(eventName, piExtensionError(err.Error()))
		return
	}
	b.dispatchEvent(eventName, result)
}

func (b *browser) sendPiExtensionStatus() {
	b.relayPiExtension(eventPiInstallStatus, protocol.MethodPiExtensionStatus)
}

func (b *browser) dispatchImportResult(ok bool, message string) {
	b.dispatchEvent(eventImportResult, map[string]any{
		"ok":      ok,
		"message": message,
	})
}

func (b *browser) dispatchEvent(name string, detail any) {
	payload, err := json.Marshal(detail)
	if err != nil {
		payload = []byte("{}")
	}
	b.evaluate(fmt.Sprintf(
		`window.dispatchEvent(new CustomEvent(%q, { detail: %s }));`,
		name,
		string(payload),
	))
}

func (b *browser) setEval(eval func(string)) {
	b.mu.Lock()
	b.eval = eval
	b.mu.Unlock()
}

func (b *browser) clearEval() {
	b.setEval(nil)
}

func (b *browser) evaluate(script string) {
	b.mu.RLock()
	eval := b.eval
	b.mu.RUnlock()
	if eval != nil {
		eval(script)
	}
}

func (b *browser) request(method string, payload any, out any) error {
	ctx, cancel := context.WithTimeout(b.ctx, b.options.RequestTimeout)
	defer cancel()
	return b.client.request(ctx, method, payload, out)
}

func (b *browser) currentRows() []protocol.BrowserPetRow {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]protocol.BrowserPetRow(nil), b.rows...)
}

func (b *browser) rowByID(id string) (protocol.BrowserPetRow, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, row := range b.rows {
		if row.ID == id {
			return row, true
		}
	}
	return protocol.BrowserPetRow{}, false
}

func (b *browser) beginImport() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.importInFlight {
		return false
	}
	b.importInFlight = true
	return true
}

func (b *browser) endImport() {
	b.mu.Lock()
	b.importInFlight = false
	b.mu.Unlock()
}

func browserRowPayload(row protocol.BrowserPetRow, assetURL func(id string, spriteName string) string) map[string]any {
	if row.ID == "" || row.DisplayName == "" {
		return nil
	}
	spriteName := firstNonEmpty(row.SpritesheetPath, "spritesheet.webp")
	source := "prebundled"
	submittedBy := row.Attribution
	if row.Installed {
		source = "installed"
		submittedBy = installedSourceLabel(row.Source)
	}
	return map[string]any{
		"source":         source,
		"nativePetId":    row.ID,
		"slug":           row.Slug,
		"displayName":    row.DisplayName,
		"description":    firstNonEmpty(row.Description, "Animated Codex-compatible pet."),
		"kind":           firstNonEmpty(row.Kind, "pet"),
		"submittedBy":    submittedBy,
		"tags":           []string{},
		"spritesheetUrl": assetURL(row.ID, spriteName),
		"frameWidth":     positiveOrDefault(row.FrameWidth, 192),
		"frameHeight":    positiveOrDefault(row.FrameHeight, 208),
		"canUninstall":   row.CanUninstall,
		"installed":      row.Installed,
	}
}

func petListEventDetail(pets []map[string]any, selectedPetID string) map[string]any {
	return map[string]any{
		"pets":          pets,
		"selectedPetId": selectedPetID,
	}
}

func installedSourceLabel(source string) string {
	switch source {
	case "app":
		return "Imported"
	case "petdex":
		return "Petdex"
	case "codex":
		return "Codex"
	default:
		return source
	}
}

func piExtensionError(message string) map[string]any {
	return map[string]any{
		"ok":          false,
		"message":     message,
		"available":   false,
		"installed":   false,
		"needsUpdate": false,
	}
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func resultName(pet *protocol.PetRef) string {
	if pet == nil {
		return ""
	}
	return pet.DisplayName
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
