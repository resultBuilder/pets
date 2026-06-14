package daemon

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codex-pets/internal/catalog"
	"codex-pets/internal/piinstall"
	"codex-pets/internal/protocol"
)

// UpdaterInterface is implemented by internal/updater; the indirection keeps
// the daemon testable with a stub pipeline.
type UpdaterInterface interface {
	Check() protocol.UpdateState
	Apply() protocol.UpdateState
	Dismiss() protocol.UpdateState
}

type Server struct {
	store *Store

	// PiExtensionSource is the bundled Pi extension file served by the
	// pi.extension.* methods. Empty means autodetect (see piinstall).
	PiExtensionSource string

	// Updater serves the update.* methods when the host runs from a git
	// checkout; nil hosts simply never expose update state.
	Updater UpdaterInterface

	// PetSources are the directories the daemon scans into the snapshot's
	// installed pets (at RefreshInstalledPets and pets.refresh). Hosts that
	// still push pets.installed.set may leave this empty.
	PetSources []catalog.InstalledRoot

	// PetImportRoot receives pet.import packages. It should also appear in
	// PetSources so imports show up on rescan.
	PetImportRoot string

	// CatalogDir is a bundled catalog directory (containing pets/<slug>
	// packages) offered in pets.browser.list next to the installed pets.
	CatalogDir string

	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
}

type subscriber struct {
	write func(protocol.Message) error
}

func NewServer(store *Store) *Server {
	if store == nil {
		store = NewStore()
	}
	return &Server{
		store:       store,
		subscribers: map[*subscriber]struct{}{},
	}
}

func DefaultSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), fmt.Sprintf("codex-pets-%d", os.Getuid()))
	}
	return filepath.Join(runtimeDir, "pi-pet.sock")
}

// DefaultStateFilePath keeps persisted daemon state in the platform config
// directory: ~/Library/Application Support on macOS, $XDG_CONFIG_HOME on Linux.
func DefaultStateFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "codex-pets", "daemon-state.json")
}

func ListenUnix(path string) (net.Listener, error) {
	if path == "" {
		path = DefaultSocketPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return listener, nil
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.acceptLoop(listener)
	}()
	go s.idlePulseLoop(ctx)
	select {
	case <-ctx.Done():
		_ = listener.Close()
		if err := <-errCh; err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (s *Server) acceptLoop(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	var writeMu sync.Mutex
	write := func(msg protocol.Message) error {
		line, err := protocol.EncodeLine(msg)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = conn.Write(line)
		return err
	}

	var sub *subscriber
	defer func() {
		if sub != nil {
			s.removeSubscriber(sub)
		}
	}()

	// Sessions attached to this connection are removed when it closes, so a
	// crashed or killed client cannot leave the pet stuck on a dead session.
	attached := map[string]struct{}{}
	defer func() {
		for id := range attached {
			snapshot := s.store.RemoveSession(protocol.SessionRemove{SessionID: id})
			s.broadcast(snapshot)
		}
	}()

	scanner := bufio.NewScanner(conn)
	// pet.import carries a base64 spritesheet (up to the 10 MiB catalog
	// limit) in a single line, so the per-line ceiling must clear that.
	scanner.Buffer(make([]byte, 0, 64*1024), 24*1024*1024)
	for scanner.Scan() {
		msg, err := protocol.DecodeLine(scanner.Bytes())
		if err != nil {
			_ = write(protocol.NewErrorResponse("", "unknown", "bad_message", err.Error()))
			continue
		}
		if msg.Kind != protocol.KindRequest {
			_ = write(protocol.NewErrorResponse(msg.ID, msg.Method, "bad_kind", "only request messages are accepted"))
			continue
		}
		keepSub, err := s.handleRequest(msg, write, &sub, attached)
		if err != nil {
			_ = write(protocol.NewErrorResponse(msg.ID, msg.Method, "request_failed", err.Error()))
		}
		if !keepSub && sub != nil {
			s.removeSubscriber(sub)
			sub = nil
		}
	}
}

func (s *Server) handleRequest(
	msg protocol.Message,
	write func(protocol.Message) error,
	sub **subscriber,
	attached map[string]struct{},
) (bool, error) {
	switch msg.Method {
	case protocol.MethodHello:
		payload, err := protocol.DecodePayload[protocol.Hello](msg)
		if err != nil {
			return true, err
		}
		if payload.Client == "" {
			payload.Client = protocol.ClientMock
		}
		return true, writeResponse(write, msg, map[string]any{"ok": true, "server": "pi-pet-daemon", "version": protocol.Version})
	case protocol.MethodSnapshotGet:
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodStateSubscribe:
		if *sub == nil {
			created := &subscriber{write: write}
			*sub = created
			s.addSubscriber(created)
		}
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodSessionUpsert:
		payload, err := protocol.DecodePayload[protocol.SessionUpsert](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.UpsertSession(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodSessionRemove:
		payload, err := protocol.DecodePayload[protocol.SessionRemove](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.RemoveSession(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodSessionAttach:
		payload, err := protocol.DecodePayload[protocol.SessionAttach](msg)
		if err != nil {
			return true, err
		}
		if id := strings.TrimSpace(payload.SessionID); id != "" {
			attached[id] = struct{}{}
		}
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodToolStart:
		payload, err := protocol.DecodePayload[protocol.ToolUpdate](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.ToolStart(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodToolUpdate:
		payload, err := protocol.DecodePayload[protocol.ToolUpdate](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.ToolUpdate(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodToolEnd:
		payload, err := protocol.DecodePayload[protocol.ToolUpdate](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.ToolEnd(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodApprovalRequest:
		payload, err := protocol.DecodePayload[protocol.ApprovalRequest](msg)
		if err != nil {
			return true, err
		}
		_, decisions, snapshot := s.store.AddApproval(payload)
		s.broadcast(snapshot)
		decision := s.waitForApproval(payload, decisions)
		if decision.Decision == protocol.ApprovalExpired {
			if ok, snapshot := s.store.ExpireApproval(decision.ApprovalID); ok {
				s.broadcast(snapshot)
			}
		}
		return true, writeResponse(write, msg, decision)
	case protocol.MethodApprovalRespond:
		payload, err := protocol.DecodePayload[protocol.ApprovalDecision](msg)
		if err != nil {
			return true, err
		}
		decision, ok, snapshot := s.store.ResolveApproval(payload)
		if !ok {
			return true, write(protocol.NewErrorResponse(msg.ID, msg.Method, "approval_not_found", "approval is not pending"))
		}
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, decision)
	case protocol.MethodPetSelect:
		payload, err := protocol.DecodePayload[protocol.PetSelect](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.SelectPet(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodInstalledPetsSet:
		payload, err := protocol.DecodePayload[protocol.InstalledPetsSet](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.SetInstalledPets(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodPetsRefresh:
		snapshot := s.RefreshInstalledPets()
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodPetsBrowserList:
		return true, writeResponse(write, msg, s.browserPetList())
	case protocol.MethodPetImport:
		payload, err := protocol.DecodePayload[protocol.PetImport](msg)
		if err != nil {
			return true, err
		}
		result := s.importPet(payload)
		if result.OK {
			s.broadcast(s.RefreshInstalledPets())
		}
		return true, writeResponse(write, msg, result)
	case protocol.MethodPetUninstall:
		payload, err := protocol.DecodePayload[protocol.PetUninstall](msg)
		if err != nil {
			return true, err
		}
		result := s.uninstallPet(payload)
		if result.OK {
			s.broadcast(s.RefreshInstalledPets())
		}
		return true, writeResponse(write, msg, result)
	case protocol.MethodCatalogSet:
		payload, err := protocol.DecodePayload[protocol.CatalogCache](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.SetCatalog(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodPiExtensionStatus:
		return true, writeResponse(write, msg, s.piExtensionResult(true, ""))
	case protocol.MethodPiExtensionInstall:
		if _, err := piinstall.Install(s.piExtensionOptions()); err != nil {
			return true, writeResponse(write, msg, s.piExtensionResult(false, err.Error()))
		}
		return true, writeResponse(write, msg, s.piExtensionResult(true, "Installed Pi extension"))
	case protocol.MethodPiExtensionUninstall:
		if _, err := piinstall.Uninstall(s.piExtensionOptions()); err != nil {
			return true, writeResponse(write, msg, s.piExtensionResult(false, err.Error()))
		}
		return true, writeResponse(write, msg, s.piExtensionResult(true, "Uninstalled Pi extension"))
	case protocol.MethodUpdateCheck:
		if s.Updater != nil {
			s.Updater.Check()
		}
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodUpdateApply:
		if s.Updater != nil {
			s.Updater.Apply()
		}
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodUpdateDismiss:
		if s.Updater != nil {
			s.Updater.Dismiss()
		}
		return true, writeResponse(write, msg, s.store.Snapshot())
	case protocol.MethodOverlayInteraction:
		payload, err := protocol.DecodePayload[protocol.OverlayInteraction](msg)
		if err != nil {
			return true, err
		}
		// No broadcast: the reaction goes back to the interacting overlay
		// only; other subscribers keep their steady presentation.
		return true, writeResponse(write, msg, s.store.HandleInteraction(payload))
	case protocol.MethodOverlaySettingsSet:
		payload, err := protocol.DecodePayload[protocol.OverlaySettings](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.SetOverlaySettings(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	case protocol.MethodMurmursMute:
		payload, err := protocol.DecodePayload[protocol.MurmursMute](msg)
		if err != nil {
			return true, err
		}
		snapshot := s.store.MuteMurmurs(payload)
		s.broadcast(snapshot)
		return true, writeResponse(write, msg, snapshot)
	default:
		return true, write(protocol.NewErrorResponse(msg.ID, msg.Method, "unknown_method", "method is not supported"))
	}
}

// RefreshInstalledPets rescans the configured pet sources (and the bundled
// catalog) into the snapshot. Hosts call it once at startup; pets.refresh
// and pet.import/pet.uninstall call it afterwards. Without configured
// sources it returns the snapshot unchanged so push-based hosts keep
// working.
func (s *Server) RefreshInstalledPets() protocol.Snapshot {
	if s.CatalogDir != "" {
		s.store.SetCatalog(protocol.CatalogCache{
			Provider: "prebundled",
			Pets: catalog.ScanInstalledPets([]catalog.InstalledRoot{
				{Dir: filepath.Join(s.CatalogDir, "pets"), Source: "prebundled"},
			}, catalog.DefaultLimits()),
		})
	}
	if len(s.PetSources) == 0 {
		return s.store.Snapshot()
	}
	pets := catalog.ScanInstalledPets(s.PetSources, catalog.DefaultLimits())
	return s.store.SetInstalledPets(protocol.InstalledPetsSet{Pets: pets})
}

// browserPetList merges installed pets with the bundled catalog into the
// picker rows every UI renders verbatim.
func (s *Server) browserPetList() protocol.BrowserPetList {
	snapshot := s.store.Snapshot()
	return protocol.BrowserPetList{
		Rows: catalog.BuildBrowserRows(
			snapshot.InstalledPets,
			snapshot.Catalogs["prebundled"].Pets,
			s.PetImportRoot,
		),
	}
}

func (s *Server) importPet(payload protocol.PetImport) protocol.PetOpResult {
	if s.PetImportRoot == "" {
		return protocol.PetOpResult{Message: "pet import is not configured on this host"}
	}
	// The ref source must match how the rescan labels the import root, or
	// the returned pet id would differ from the snapshot's.
	source := payload.Source
	if source == "" {
		source = s.importRootSource()
	}

	var ref protocol.PetRef
	var err error
	if payload.Directory != "" {
		ref, err = catalog.ImportPetFromDirectory(payload.Directory, s.PetImportRoot, source, catalog.DefaultLimits())
	} else {
		var sprite []byte
		sprite, err = base64.StdEncoding.DecodeString(payload.SpritesheetBase64)
		if err != nil {
			return protocol.PetOpResult{Message: "spritesheet is not valid base64"}
		}
		ref, err = catalog.ImportPet(catalog.ImportRequest{
			Root:           s.PetImportRoot,
			Source:         source,
			PetJSON:        []byte(payload.PetJSON),
			Spritesheet:    sprite,
			SpritesheetExt: payload.SpritesheetExt,
		}, catalog.DefaultLimits())
	}
	if err != nil {
		return protocol.PetOpResult{Message: err.Error()}
	}
	return protocol.PetOpResult{OK: true, Message: "Imported " + ref.DisplayName, Pet: &ref}
}

func (s *Server) importRootSource() string {
	for _, root := range s.PetSources {
		if root.Dir == s.PetImportRoot {
			return root.Source
		}
	}
	return "petdex"
}

func (s *Server) uninstallPet(payload protocol.PetUninstall) protocol.PetOpResult {
	var found *protocol.PetRef
	for _, pet := range s.store.Snapshot().InstalledPets {
		if pet.ID == payload.PetID {
			ref := pet
			found = &ref
			break
		}
	}
	if found == nil {
		return protocol.PetOpResult{Message: "installed pet was not found"}
	}
	if err := catalog.UninstallPet(s.PetImportRoot, found.Path); err != nil {
		return protocol.PetOpResult{Message: err.Error(), Pet: found}
	}
	return protocol.PetOpResult{OK: true, Message: "Uninstalled " + found.DisplayName, Pet: found}
}

func (s *Server) piExtensionOptions() piinstall.Options {
	return piinstall.Options{SourcePath: s.PiExtensionSource}
}

func (s *Server) piExtensionResult(ok bool, message string) protocol.PiExtensionResult {
	status := piinstall.GetStatus(s.piExtensionOptions())
	if message == "" {
		message = status.Message
	}
	return protocol.PiExtensionResult{
		OK:               ok,
		Message:          message,
		Available:        status.Available,
		Installed:        status.Installed,
		NeedsUpdate:      status.NeedsUpdate,
		Path:             status.Path,
		SourceVersion:    status.SourceVersion,
		InstalledVersion: status.InstalledVersion,
	}
}

func (s *Server) waitForApproval(input protocol.ApprovalRequest, decisions <-chan protocol.ApprovalDecision) protocol.ApprovalDecision {
	timeout := 10 * time.Minute
	if input.TimeoutMillis > 0 {
		timeout = time.Duration(input.TimeoutMillis) * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case decision, ok := <-decisions:
		if ok {
			return decision
		}
	case <-timer.C:
	}
	id := input.ApprovalID
	if id == "" {
		id = input.SessionID + ":" + input.ToolCallID
	}
	return protocol.ApprovalDecision{ApprovalID: id, Decision: protocol.ApprovalExpired, Reason: "approval timed out"}
}

// PublishSnapshot broadcasts a snapshot produced outside a request handler,
// such as asynchronous update-pipeline progress.
func (s *Server) PublishSnapshot(snapshot protocol.Snapshot) {
	s.broadcast(snapshot)
}

// idlePulseLoop drives the brain's ambient behavior (rare idle murmurs,
// long-running settle) while the server runs.
func (s *Server) idlePulseLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if snapshot, changed := s.store.IdlePulse(); changed {
				s.broadcast(snapshot)
			}
		}
	}
}

func (s *Server) addSubscriber(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers[sub] = struct{}{}
}

func (s *Server) removeSubscriber(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers, sub)
}

func (s *Server) broadcast(snapshot protocol.Snapshot) {
	event, err := protocol.NewEvent(protocol.EventSnapshot, snapshot)
	if err != nil {
		return
	}
	s.mu.Lock()
	subscribers := make([]*subscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subscribers = append(subscribers, sub)
	}
	s.mu.Unlock()
	for _, sub := range subscribers {
		if err := sub.write(event); err != nil {
			s.removeSubscriber(sub)
		}
	}
}

func writeResponse(write func(protocol.Message) error, request protocol.Message, payload any) error {
	response, err := protocol.NewResponse(request.ID, request.Method, payload)
	if err != nil {
		return err
	}
	return write(response)
}

func ReadMessage(r io.Reader) (protocol.Message, error) {
	reader := bufio.NewReader(r)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return protocol.Message{}, err
	}
	return protocol.DecodeLine(line)
}
