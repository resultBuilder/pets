package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-pets/internal/catalog"
	petoverlay "codex-pets/internal/overlay"
	"codex-pets/internal/petbrain"
	"codex-pets/internal/protocol"
)

type testClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func newTestClient(t *testing.T, socketPath string) *testClient {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	return &testClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *testClient) close() {
	_ = c.conn.Close()
}

func (c *testClient) request(t *testing.T, id string, method string, payload any) protocol.Message {
	t.Helper()
	msg, err := protocol.NewRequest(id, method, payload)
	if err != nil {
		t.Fatalf("request message: %v", err)
	}
	line, err := protocol.EncodeLine(msg)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := c.conn.Write(line); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return c.read(t)
}

func (c *testClient) read(t *testing.T) protocol.Message {
	t.Helper()
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	msg, err := protocol.DecodeLine(line)
	if err != nil {
		t.Fatalf("decode response: %v\n%s", err, line)
	}
	if msg.Error != nil {
		t.Fatalf("protocol error: %s: %s", msg.Error.Code, msg.Error.Message)
	}
	return msg
}

func startTestServer(t *testing.T) (string, context.CancelFunc) {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "pi-pet-test-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "pi.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(NewStore())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ctx, listener)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	})
	return socketPath, cancel
}

func TestPiExtensionDaemonOverlayStateFlow(t *testing.T) {
	socketPath, _ := startTestServer(t)
	overlay := newTestClient(t, socketPath)
	defer overlay.close()
	extension := newTestClient(t, socketPath)
	defer extension.close()

	subscribe := overlay.request(t, "sub", protocol.MethodStateSubscribe, nil)
	var initial protocol.Snapshot
	if err := json.Unmarshal(subscribe.Payload, &initial); err != nil {
		t.Fatalf("decode initial snapshot: %v", err)
	}
	if initial.Attention != protocol.AttentionIdle {
		t.Fatalf("initial attention = %s, want idle", initial.Attention)
	}

	extension.request(t, "s1", protocol.MethodSessionUpsert, protocol.SessionUpsert{
		SessionID:   "pi-session-1",
		CWD:         "/repo",
		Title:       "Implement feature",
		Status:      protocol.SessionRunning,
		SafeSummary: "agent running",
	})

	event := overlay.read(t)
	if event.Kind != protocol.KindEvent || event.Method != protocol.EventSnapshot {
		t.Fatalf("unexpected event: %+v", event)
	}
	var snapshot protocol.Snapshot
	if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
		t.Fatalf("decode event snapshot: %v", err)
	}
	if snapshot.Attention != protocol.AttentionRunning {
		t.Fatalf("attention = %s, want running", snapshot.Attention)
	}
	if presentation := petoverlay.Present(snapshot); presentation.StateID != "running" {
		t.Fatalf("overlay state = %s, want running", presentation.StateID)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != "pi-session-1" {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
}

func TestToolUpdateMethodRefreshesRunningTool(t *testing.T) {
	socketPath, _ := startTestServer(t)
	extension := newTestClient(t, socketPath)
	defer extension.close()

	extension.request(t, "tool-start", protocol.MethodToolStart, protocol.ToolUpdate{
		SessionID:   "pi-session-1",
		ToolCallID:  "tool-1",
		ToolName:    "bash",
		SafeSummary: "bash started",
	})
	response := extension.request(t, "tool-update", protocol.MethodToolUpdate, protocol.ToolUpdate{
		SessionID:   "pi-session-1",
		ToolCallID:  "tool-1",
		ToolName:    "bash",
		SafeSummary: "bash running",
	})

	var snapshot protocol.Snapshot
	if err := json.Unmarshal(response.Payload, &snapshot); err != nil {
		t.Fatalf("decode tool update snapshot: %v", err)
	}
	if len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Tools) != 1 {
		t.Fatalf("snapshot tools = %+v", snapshot.Sessions)
	}
	tool := snapshot.Sessions[0].Tools[0]
	if tool.State != protocol.ToolRunning || tool.SafeSummary != "bash running" {
		t.Fatalf("tool after update = %+v, want running progress summary", tool)
	}
}

func TestAttachedSessionRemovedWhenConnectionDrops(t *testing.T) {
	socketPath, _ := startTestServer(t)
	extension := newTestClient(t, socketPath)
	browser := newTestClient(t, socketPath)
	defer browser.close()

	extension.request(t, "attach", protocol.MethodSessionAttach, protocol.SessionAttach{SessionID: "pi-session-1"})
	extension.request(t, "upsert", protocol.MethodSessionUpsert, protocol.SessionUpsert{
		SessionID:   "pi-session-1",
		Status:      protocol.SessionRunning,
		SafeSummary: "turn 1 running",
	})

	approvalCh := make(chan protocol.ApprovalDecision, 1)
	go func() {
		message, err := goldenRawRequest(socketPath, "approval", protocol.MethodApprovalRequest, mustPayload(t, protocol.ApprovalRequest{
			ApprovalID:    "pi-session-1:tool-1",
			SessionID:     "pi-session-1",
			ToolCallID:    "tool-1",
			ToolName:      "bash",
			TimeoutMillis: 5000,
		}))
		if err != nil {
			t.Errorf("approval request: %v", err)
			return
		}
		var decision protocol.ApprovalDecision
		if err := json.Unmarshal(message.Payload, &decision); err != nil {
			t.Errorf("decode approval decision: %v", err)
			return
		}
		approvalCh <- decision
	}()

	waitForSnapshot(t, browser, func(snapshot protocol.Snapshot) bool {
		return len(snapshot.PendingApprovals) == 1
	})

	// Simulate a killed pi process: the attach connection drops without an
	// explicit session.remove.
	extension.close()

	waitForSnapshot(t, browser, func(snapshot protocol.Snapshot) bool {
		return len(snapshot.Sessions) == 0 && snapshot.Attention == protocol.AttentionIdle && len(snapshot.PendingApprovals) == 0
	})

	select {
	case decision := <-approvalCh:
		if decision.Decision != protocol.ApprovalExpired || decision.Reason != "session terminated" {
			t.Fatalf("decision = %+v, want expired/session terminated", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval request was not unblocked by the dropped attach connection")
	}
}

func waitForSnapshot(t *testing.T, client *testClient, predicate func(protocol.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		response := client.request(t, fmt.Sprintf("wait-%d", time.Now().UnixNano()), protocol.MethodSnapshotGet, nil)
		var snapshot protocol.Snapshot
		if err := json.Unmarshal(response.Payload, &snapshot); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if predicate(snapshot) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot never matched, last: %+v", snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustPayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	raw, err := protocol.MarshalPayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

func TestPiExtensionMethodsOverSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sourcePath := filepath.Join(t.TempDir(), "index.ts")
	source := `export const CODEX_PETS_PI_EXTENSION_VERSION = "9.9.9";`
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	socketDir, err := os.MkdirTemp("/tmp", "pi-pet-test-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "pi.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(NewStore())
	server.PiExtensionSource = sourcePath
	go func() {
		_ = server.Serve(ctx, listener)
	}()

	browser := newTestClient(t, socketPath)
	defer browser.close()

	decodeResult := func(msg protocol.Message) protocol.PiExtensionResult {
		t.Helper()
		var result protocol.PiExtensionResult
		if err := json.Unmarshal(msg.Payload, &result); err != nil {
			t.Fatalf("decode pi extension result: %v", err)
		}
		return result
	}

	status := decodeResult(browser.request(t, "status", protocol.MethodPiExtensionStatus, nil))
	if !status.OK || !status.Available || status.Installed {
		t.Fatalf("initial status = %+v, want available and not installed", status)
	}
	if status.SourceVersion != "9.9.9" {
		t.Fatalf("source version = %q, want 9.9.9", status.SourceVersion)
	}

	installed := decodeResult(browser.request(t, "install", protocol.MethodPiExtensionInstall, nil))
	if !installed.OK || !installed.Installed || installed.NeedsUpdate {
		t.Fatalf("install result = %+v, want installed", installed)
	}
	if installed.InstalledVersion != "9.9.9" {
		t.Fatalf("installed version = %q, want 9.9.9", installed.InstalledVersion)
	}
	if _, err := os.Stat(installed.Path); err != nil {
		t.Fatalf("installed extension file missing: %v", err)
	}

	removed := decodeResult(browser.request(t, "uninstall", protocol.MethodPiExtensionUninstall, nil))
	if !removed.OK || removed.Installed {
		t.Fatalf("uninstall result = %+v, want not installed", removed)
	}
	if _, err := os.Stat(installed.Path); !os.IsNotExist(err) {
		t.Fatalf("installed extension still exists: %v", err)
	}
}

func TestApprovalRequestBlocksUntilBrowserResponse(t *testing.T) {
	socketPath, _ := startTestServer(t)
	extension := newTestClient(t, socketPath)
	defer extension.close()
	browser := newTestClient(t, socketPath)
	defer browser.close()

	resultCh := make(chan protocol.Message, 1)
	go func() {
		resultCh <- extension.request(t, "approval", protocol.MethodApprovalRequest, protocol.ApprovalRequest{
			ApprovalID:     "approval-1",
			SessionID:      "pi-session-1",
			ToolCallID:     "tool-1",
			ToolName:       "bash",
			CommandSummary: "git status --short",
			Risk:           "read-only",
			TimeoutMillis:  5000,
		})
	}()

	deadline := time.After(1500 * time.Millisecond)
	for {
		snapshotResponse := browser.request(t, "snapshot", protocol.MethodSnapshotGet, nil)
		var snapshot protocol.Snapshot
		if err := json.Unmarshal(snapshotResponse.Payload, &snapshot); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if len(snapshot.PendingApprovals) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("approval request did not become pending")
		case <-time.After(20 * time.Millisecond):
		}
	}

	browser.request(t, "respond", protocol.MethodApprovalRespond, protocol.ApprovalDecision{
		ApprovalID: "approval-1",
		Decision:   protocol.ApprovalApproved,
		Reason:     "safe read",
	})

	select {
	case msg := <-resultCh:
		var decision protocol.ApprovalDecision
		if err := json.Unmarshal(msg.Payload, &decision); err != nil {
			t.Fatalf("decode decision: %v", err)
		}
		if decision.Decision != protocol.ApprovalApproved {
			t.Fatalf("decision = %s, want approved", decision.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("extension did not receive approval response")
	}
}

func writeTestPetPackage(t *testing.T, root string, dirName string, slug string, displayName string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spritesheet.png"), testPetSpritePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"slug": %q, "displayName": %q, "spritesheetPath": "spritesheet.png", "frameWidth": 4, "frameHeight": 3, "license": "MIT"}`, slug, displayName)
	if err := os.WriteFile(filepath.Join(dir, "pet.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testPetSpritePNG(t *testing.T) []byte {
	t.Helper()
	atlas := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for offset := 3; offset < len(atlas.Pix); offset += 4 {
		atlas.Pix[offset] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, atlas); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPetLifecycleOverSocket(t *testing.T) {
	appRoot := t.TempDir()
	importRoot := filepath.Join(t.TempDir(), "imported")
	writeTestPetPackage(t, appRoot, "boba", "boba", "Boba")

	socketDir, err := os.MkdirTemp("/tmp", "pi-pet-test-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "pi.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(NewStore())
	server.PetSources = []catalog.InstalledRoot{
		{Dir: appRoot, Source: "app"},
		{Dir: importRoot, Source: "petdex"},
	}
	server.PetImportRoot = importRoot
	if snapshot := server.RefreshInstalledPets(); len(snapshot.InstalledPets) != 1 {
		t.Fatalf("startup scan pets = %+v, want boba", snapshot.InstalledPets)
	}
	go func() {
		_ = server.Serve(ctx, listener)
	}()

	browser := newTestClient(t, socketPath)
	defer browser.close()

	decodeOp := func(msg protocol.Message) protocol.PetOpResult {
		t.Helper()
		var result protocol.PetOpResult
		if err := json.Unmarshal(msg.Payload, &result); err != nil {
			t.Fatalf("decode pet op result: %v", err)
		}
		return result
	}

	imported := decodeOp(browser.request(t, "import", protocol.MethodPetImport, protocol.PetImport{
		PetJSON:           `{"slug": "zorro-legacy", "displayName": "Zorro", "frameWidth": 4, "frameHeight": 3, "license": "MIT"}`,
		SpritesheetBase64: base64.StdEncoding.EncodeToString(testPetSpritePNG(t)),
		SpritesheetExt:    "png",
	}))
	if !imported.OK || imported.Pet == nil {
		t.Fatalf("import result = %+v", imported)
	}
	if imported.Pet.ID != "petdex:zorro:"+filepath.Join(importRoot, "zorro") {
		t.Fatalf("imported pet id = %q", imported.Pet.ID)
	}
	waitForSnapshot(t, browser, func(snapshot protocol.Snapshot) bool {
		return len(snapshot.InstalledPets) == 2
	})

	// Import the same pet under another legacy slug: dedupe keeps one row.
	again := decodeOp(browser.request(t, "import-2", protocol.MethodPetImport, protocol.PetImport{
		PetJSON:           `{"slug": "zorro-2", "displayName": "Zorro", "frameWidth": 4, "frameHeight": 3, "license": "MIT"}`,
		SpritesheetBase64: base64.StdEncoding.EncodeToString(testPetSpritePNG(t)),
		SpritesheetExt:    "png",
	}))
	if !again.OK {
		t.Fatalf("re-import result = %+v", again)
	}
	waitForSnapshot(t, browser, func(snapshot protocol.Snapshot) bool {
		return len(snapshot.InstalledPets) == 2
	})

	// Bundled pets cannot be uninstalled; imported ones can.
	var snapshot protocol.Snapshot
	if err := json.Unmarshal(browser.request(t, "snap", protocol.MethodSnapshotGet, nil).Payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	var bundledID string
	for _, pet := range snapshot.InstalledPets {
		if pet.Source == "app" {
			bundledID = pet.ID
		}
	}
	if blocked := decodeOp(browser.request(t, "rm-bundled", protocol.MethodPetUninstall, protocol.PetUninstall{PetID: bundledID})); blocked.OK {
		t.Fatalf("bundled pet uninstall should fail, got %+v", blocked)
	}
	removed := decodeOp(browser.request(t, "rm", protocol.MethodPetUninstall, protocol.PetUninstall{PetID: imported.Pet.ID}))
	if !removed.OK {
		t.Fatalf("uninstall result = %+v", removed)
	}
	waitForSnapshot(t, browser, func(snapshot protocol.Snapshot) bool {
		return len(snapshot.InstalledPets) == 1
	})
	if _, err := os.Stat(filepath.Join(importRoot, "zorro")); !os.IsNotExist(err) {
		t.Fatal("imported pet directory should be removed")
	}

	// pets.refresh notices packages dropped in from outside the protocol.
	writeTestPetPackage(t, appRoot, "pepe", "pepe", "Pepe")
	var refreshed protocol.Snapshot
	if err := json.Unmarshal(browser.request(t, "refresh", protocol.MethodPetsRefresh, nil).Payload, &refreshed); err != nil {
		t.Fatal(err)
	}
	if len(refreshed.InstalledPets) != 2 {
		t.Fatalf("refresh pets = %+v, want boba+pepe", refreshed.InstalledPets)
	}
}

func TestBrowserListAndDirectoryImportOverSocket(t *testing.T) {
	catalogDir := t.TempDir()
	importRoot := filepath.Join(t.TempDir(), "imported")
	writeTestPetPackage(t, filepath.Join(catalogDir, "pets"), "boba", "boba", "Boba")
	writeTestPetPackage(t, filepath.Join(catalogDir, "pets"), "cappy", "cappy", "Cappy")

	socketDir, err := os.MkdirTemp("/tmp", "pi-pet-test-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "pi.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(NewStore())
	server.PetSources = []catalog.InstalledRoot{{Dir: importRoot, Source: "app"}}
	server.PetImportRoot = importRoot
	server.CatalogDir = catalogDir
	server.RefreshInstalledPets()
	go func() {
		_ = server.Serve(ctx, listener)
	}()

	browser := newTestClient(t, socketPath)
	defer browser.close()

	decodeRows := func(msg protocol.Message) []protocol.BrowserPetRow {
		t.Helper()
		var list protocol.BrowserPetList
		if err := json.Unmarshal(msg.Payload, &list); err != nil {
			t.Fatalf("decode browser list: %v", err)
		}
		return list.Rows
	}

	rows := decodeRows(browser.request(t, "list", protocol.MethodPetsBrowserList, nil))
	if len(rows) != 2 || rows[0].Installed || rows[1].Installed {
		t.Fatalf("initial rows = %+v, want 2 catalog rows", rows)
	}
	if rows[0].SpritesheetPath == "" || rows[0].FrameWidth == 0 || rows[0].Slug == "" {
		t.Fatalf("rows are not enriched: %+v", rows[0])
	}

	// Import a catalog pet by its package directory (what the picker does).
	var bobaPath string
	for _, row := range rows {
		if row.Slug == "boba" {
			bobaPath = row.Path
		}
	}
	var imported protocol.PetOpResult
	if err := json.Unmarshal(browser.request(t, "import", protocol.MethodPetImport, protocol.PetImport{Directory: bobaPath}).Payload, &imported); err != nil {
		t.Fatal(err)
	}
	if !imported.OK || imported.Pet == nil || imported.Pet.Source != "app" {
		t.Fatalf("directory import result = %+v", imported)
	}

	rows = decodeRows(browser.request(t, "list-2", protocol.MethodPetsBrowserList, nil))
	if len(rows) != 2 {
		t.Fatalf("rows after import = %+v, want installed boba + catalog cappy", rows)
	}
	if !rows[0].Installed || !rows[0].CanUninstall || rows[0].Slug != "boba" {
		t.Fatalf("first row should be the imported uninstallable boba: %+v", rows[0])
	}
	if rows[1].Installed || rows[1].Slug != "cappy" {
		t.Fatalf("catalog boba must be hidden behind the installed one: %+v", rows[1])
	}
}

func TestBrainInteractionsOverSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "pi-pet-test-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "pi.sock")
	listener, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewStore()
	server := NewServer(store)
	store.AttachBrain(petbrain.New(petbrain.Options{Random: func() float64 { return 0 }}), server.PublishSnapshot)
	go func() {
		_ = server.Serve(ctx, listener)
	}()

	overlay := newTestClient(t, socketPath)
	defer overlay.close()

	decodeSnapshot := func(msg protocol.Message) protocol.Snapshot {
		t.Helper()
		var snapshot protocol.Snapshot
		if err := json.Unmarshal(msg.Payload, &snapshot); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		return snapshot
	}

	decodeResult := func(msg protocol.Message) protocol.InteractionResult {
		t.Helper()
		var result protocol.InteractionResult
		if err := json.Unmarshal(msg.Payload, &result); err != nil {
			t.Fatalf("decode interaction result: %v", err)
		}
		return result
	}

	clicked := decodeResult(overlay.request(t, "click", protocol.MethodOverlayInteraction, protocol.OverlayInteraction{
		Type:   protocol.InteractionClick,
		Clicks: 1,
	}))
	if clicked.StateID != "waving" || clicked.DurationSeconds <= 0 {
		t.Fatalf("click should suggest a short waving pose, got %+v", clicked)
	}
	if clicked.Murmur == "" {
		t.Fatalf("click should murmur: %+v", clicked)
	}

	// The shared presentation must stay untouched: interactions are
	// renderer-local and never mask agent states for other overlays.
	steady := decodeSnapshot(overlay.request(t, "snap", protocol.MethodSnapshotGet, nil))
	if steady.Presentation.StateID != "idle" || steady.Presentation.Bubble != "" {
		t.Fatalf("interaction leaked into the shared presentation: %+v", steady.Presentation)
	}

	muted := decodeSnapshot(overlay.request(t, "mute", protocol.MethodMurmursMute, protocol.MurmursMute{Today: true}))
	if muted.Presentation.Bubble != "" {
		t.Fatalf("muting should clear murmurs, got %q", muted.Presentation.Bubble)
	}
	dragged := decodeResult(overlay.request(t, "drag", protocol.MethodOverlayInteraction, protocol.OverlayInteraction{
		Type: protocol.InteractionDrag,
	}))
	if dragged.Murmur != "" {
		t.Fatalf("muted pet must not murmur on drag, got %q", dragged.Murmur)
	}

	settings := decodeSnapshot(overlay.request(t, "settings", protocol.MethodOverlaySettingsSet, protocol.OverlaySettings{
		AttentionMode: "focus",
		BubbleMode:    "off",
	}))
	if settings.Presentation.StateID == "" {
		t.Fatalf("settings response should carry a snapshot: %+v", settings.Presentation)
	}
}
