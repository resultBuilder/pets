package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codex-pets/internal/catalog"
	"codex-pets/internal/overlay"
	"codex-pets/internal/petbrain"
	"codex-pets/internal/protocol"
)

const maxSafeSummaryLength = 180

type Store struct {
	mu        sync.RWMutex
	now       func() time.Time
	stateFile string
	snapshot  protocol.Snapshot
	waiters   map[string]chan protocol.ApprovalDecision
	completed map[string]protocol.ApprovalDecision
	removed   map[string]struct{}

	// brain adds murmurs and interaction reactions to the presentation;
	// nil keeps the store purely mechanical (tests, push-only hosts).
	brain             *petbrain.Brain
	pulse             *presentationPulse
	pulseTimer        *time.Timer
	asyncBroadcast    func(protocol.Snapshot)
	persistedSettings persistedState
}

// presentationPulse keeps a passive murmur (ambient, late night, codex
// transition) visible across unrelated snapshot mutations until it expires.
// It never carries a pose: interactive reactions are renderer-local.
type presentationPulse struct {
	bubble string
	until  time.Time
}

// persistedState is the daemon state that survives restarts. Sessions,
// tools, and approvals are intentionally ephemeral.
type persistedState struct {
	SelectedPetID string `json:"selectedPetId,omitempty"`
	AttentionMode string `json:"attentionMode,omitempty"`
	BubbleMode    string `json:"bubbleMode,omitempty"`
	ReduceMotion  bool   `json:"reduceMotion,omitempty"`
}

func NewStore() *Store {
	return NewStoreWithClock(time.Now)
}

// NewStoreWithStateFile returns a store that restores the selected pet from
// path and persists future selections there. An empty path disables
// persistence (used by tests and headless runs).
func NewStoreWithStateFile(path string) *Store {
	store := NewStore()
	store.stateFile = path
	store.loadPersistedState()
	return store
}

func (s *Store) loadPersistedState() {
	if s.stateFile == "" {
		return
	}
	data, err := os.ReadFile(s.stateFile)
	if err != nil {
		return
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}
	s.snapshot.SelectedPetID = clamp(state.SelectedPetID, 160)
	s.persistedSettings = state
}

func (s *Store) savePersistedStateLocked() {
	if s.stateFile == "" {
		return
	}
	state := s.persistedSettings
	state.SelectedPetID = s.snapshot.SelectedPetID
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		return
	}
	tempPath := s.stateFile + ".tmp"
	if err := os.WriteFile(tempPath, append(data, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tempPath, s.stateFile)
}

func NewStoreWithClock(now func() time.Time) *Store {
	ts := now().UTC()
	snapshot := protocol.Snapshot{
		Attention:        protocol.AttentionIdle,
		Sessions:         []protocol.Session{},
		PendingApprovals: []protocol.PendingApproval{},
		InstalledPets:    []protocol.PetRef{},
		Catalogs:         map[string]protocol.CatalogCache{},
		UpdatedAt:        ts,
	}
	snapshot.Presentation = overlay.BuildPresentation(snapshot)
	return &Store{
		now:       now,
		snapshot:  snapshot,
		waiters:   map[string]chan protocol.ApprovalDecision{},
		completed: map[string]protocol.ApprovalDecision{},
		removed:   map[string]struct{}{},
	}
}

func (s *Store) Snapshot() protocol.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.snapshot)
}

func (s *Store) UpsertSession(input protocol.SessionUpsert) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := s.now().UTC()
	id := strings.TrimSpace(input.SessionID)
	if id == "" {
		id = "default"
	}
	delete(s.removed, id)
	status := protocol.NormalizeStatus(input.Status)
	found := false
	for i := range s.snapshot.Sessions {
		if s.snapshot.Sessions[i].ID == id {
			s.snapshot.Sessions[i].CWD = clamp(input.CWD, 240)
			s.snapshot.Sessions[i].Title = clamp(input.Title, 120)
			s.snapshot.Sessions[i].Status = status
			s.snapshot.Sessions[i].SafeSummary = safeSummary(input.SafeSummary)
			s.snapshot.Sessions[i].UpdatedAt = ts
			found = true
			break
		}
	}
	if !found {
		s.snapshot.Sessions = append(s.snapshot.Sessions, protocol.Session{
			ID:          id,
			CWD:         clamp(input.CWD, 240),
			Title:       clamp(input.Title, 120),
			Status:      status,
			SafeSummary: safeSummary(input.SafeSummary),
			StartedAt:   ts,
			UpdatedAt:   ts,
		})
	}
	s.finishMutationLocked(ts)
	return cloneSnapshot(s.snapshot)
}

func (s *Store) RemoveSession(input protocol.SessionRemove) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := strings.TrimSpace(input.SessionID)
	if id == "" {
		return cloneSnapshot(s.snapshot)
	}
	s.removed[id] = struct{}{}
	next := s.snapshot.Sessions[:0]
	for _, session := range s.snapshot.Sessions {
		if session.ID != id {
			next = append(next, session)
		}
	}
	s.snapshot.Sessions = next

	nextApprovals := s.snapshot.PendingApprovals[:0]
	for _, pending := range s.snapshot.PendingApprovals {
		if pending.SessionID == id {
			decision := protocol.ApprovalDecision{
				ApprovalID: pending.ID,
				Decision:   protocol.ApprovalExpired,
				Reason:     "session terminated",
			}
			s.completed[pending.ID] = decision
			if ch := s.waiters[pending.ID]; ch != nil {
				ch <- decision
				close(ch)
				delete(s.waiters, pending.ID)
			}
			continue
		}
		nextApprovals = append(nextApprovals, pending)
	}
	s.snapshot.PendingApprovals = nextApprovals

	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

func (s *Store) ToolStart(input protocol.ToolUpdate) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := s.now().UTC()
	sessionID := normalizedSessionID(input.SessionID)
	if s.isRemovedSessionLocked(sessionID) {
		return cloneSnapshot(s.snapshot)
	}
	session := s.ensureSessionLocked(sessionID, ts)
	toolID := strings.TrimSpace(input.ToolCallID)
	if toolID == "" {
		toolID = input.ToolName + "-" + ts.Format("150405.000000000")
	}

	for i := range session.Tools {
		if session.Tools[i].ID == toolID {
			session.Tools[i].Name = clamp(input.ToolName, 80)
			session.Tools[i].State = protocol.ToolRunning
			session.Tools[i].SafeSummary = safeSummary(input.SafeSummary)
			session.Tools[i].StartedAt = ts
			session.Tools[i].EndedAt = time.Time{}
			session.UpdatedAt = ts
			s.finishMutationLocked(ts)
			return cloneSnapshot(s.snapshot)
		}
	}
	session.Tools = append(session.Tools, protocol.ToolRun{
		ID:          toolID,
		SessionID:   session.ID,
		Name:        clamp(input.ToolName, 80),
		State:       protocol.ToolRunning,
		SafeSummary: safeSummary(input.SafeSummary),
		StartedAt:   ts,
	})
	session.Status = protocol.SessionRunning
	session.UpdatedAt = ts
	s.finishMutationLocked(ts)
	return cloneSnapshot(s.snapshot)
}

func (s *Store) ToolUpdate(input protocol.ToolUpdate) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := s.now().UTC()
	sessionID := normalizedSessionID(input.SessionID)
	if s.isRemovedSessionLocked(sessionID) {
		return cloneSnapshot(s.snapshot)
	}
	session := s.ensureSessionLocked(sessionID, ts)
	toolID := strings.TrimSpace(input.ToolCallID)
	if toolID == "" {
		toolID = input.ToolName + "-" + ts.Format("150405.000000000")
	}
	name := clamp(input.ToolName, 80)
	summary := safeSummary(input.SafeSummary)
	for i := range session.Tools {
		if session.Tools[i].ID == toolID {
			if name != "" {
				session.Tools[i].Name = name
			}
			if session.Tools[i].State == "" || session.Tools[i].State == protocol.ToolRunning {
				session.Tools[i].State = protocol.ToolRunning
			}
			if summary != "" {
				session.Tools[i].SafeSummary = summary
			}
			session.UpdatedAt = ts
			s.finishMutationLocked(ts)
			return cloneSnapshot(s.snapshot)
		}
	}
	session.Tools = append(session.Tools, protocol.ToolRun{
		ID:          toolID,
		SessionID:   session.ID,
		Name:        name,
		State:       protocol.ToolRunning,
		SafeSummary: summary,
		StartedAt:   ts,
	})
	session.Status = protocol.SessionRunning
	session.UpdatedAt = ts
	s.finishMutationLocked(ts)
	return cloneSnapshot(s.snapshot)
}

func (s *Store) ToolEnd(input protocol.ToolUpdate) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := s.now().UTC()
	sessionID := normalizedSessionID(input.SessionID)
	if s.isRemovedSessionLocked(sessionID) {
		return cloneSnapshot(s.snapshot)
	}
	session := s.ensureSessionLocked(sessionID, ts)
	state := protocol.NormalizeToolState(input.State)
	if state == protocol.ToolRunning {
		state = protocol.ToolDone
	}
	for i := range session.Tools {
		if session.Tools[i].ID == input.ToolCallID {
			session.Tools[i].State = state
			session.Tools[i].SafeSummary = safeSummary(input.SafeSummary)
			session.Tools[i].EndedAt = ts
			break
		}
	}
	if state == protocol.ToolFailed {
		session.Status = protocol.SessionFailed
	}
	session.UpdatedAt = ts
	s.finishMutationLocked(ts)
	return cloneSnapshot(s.snapshot)
}

func (s *Store) AddApproval(input protocol.ApprovalRequest) (protocol.PendingApproval, <-chan protocol.ApprovalDecision, protocol.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := s.now().UTC()
	sessionID := clamp(input.SessionID, 120)
	toolCallID := clamp(input.ToolCallID, 120)
	id := strings.TrimSpace(input.ApprovalID)
	if id == "" {
		id = sessionID + ":" + toolCallID
	}
	if id == ":" {
		id = "approval-" + ts.Format("150405.000000000")
	}

	if _, removed := s.removed[sessionID]; sessionID != "" && removed {
		decision := protocol.ApprovalDecision{
			ApprovalID: id,
			Decision:   protocol.ApprovalExpired,
			Reason:     "session terminated",
		}
		ch := make(chan protocol.ApprovalDecision, 1)
		ch <- decision
		close(ch)
		s.completed[id] = decision
		s.finishMutationLocked(ts)
		return protocol.PendingApproval{
			ID:         id,
			SessionID:  sessionID,
			ToolCallID: toolCallID,
			ToolName:   clamp(input.ToolName, 80),
			State:      protocol.ApprovalExpired,
			CreatedAt:  ts,
			UpdatedAt:  ts,
		}, ch, cloneSnapshot(s.snapshot)
	}

	pending := protocol.PendingApproval{
		ID:             id,
		SessionID:      sessionID,
		ToolCallID:     toolCallID,
		ToolName:       clamp(input.ToolName, 80),
		CommandSummary: safeSummary(input.CommandSummary),
		Risk:           clamp(input.Risk, 80),
		State:          protocol.ApprovalPending,
		CreatedAt:      ts,
		UpdatedAt:      ts,
	}

	replaced := false
	for i := range s.snapshot.PendingApprovals {
		if s.snapshot.PendingApprovals[i].ID == id {
			s.snapshot.PendingApprovals[i] = pending
			replaced = true
			break
		}
	}
	if !replaced {
		s.snapshot.PendingApprovals = append(s.snapshot.PendingApprovals, pending)
	}
	ch := make(chan protocol.ApprovalDecision, 1)
	s.waiters[id] = ch
	s.finishMutationLocked(ts)
	return pending, ch, cloneSnapshot(s.snapshot)
}

func (s *Store) ResolveApproval(input protocol.ApprovalDecision) (protocol.ApprovalDecision, bool, protocol.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	decision, ok := protocol.NormalizeDecision(input.Decision)
	if !ok {
		return protocol.ApprovalDecision{}, false, cloneSnapshot(s.snapshot)
	}
	id := strings.TrimSpace(input.ApprovalID)
	if id == "" {
		return protocol.ApprovalDecision{}, false, cloneSnapshot(s.snapshot)
	}
	resolved := protocol.ApprovalDecision{
		ApprovalID: id,
		Decision:   decision,
		Reason:     safeSummary(input.Reason),
	}

	next := s.snapshot.PendingApprovals[:0]
	found := false
	for _, pending := range s.snapshot.PendingApprovals {
		if pending.ID == id {
			found = true
			continue
		}
		next = append(next, pending)
	}
	if !found {
		return protocol.ApprovalDecision{}, false, cloneSnapshot(s.snapshot)
	}
	s.snapshot.PendingApprovals = next
	s.completed[id] = resolved
	if ch := s.waiters[id]; ch != nil {
		ch <- resolved
		close(ch)
		delete(s.waiters, id)
	}
	s.finishMutationLocked(s.now().UTC())
	return resolved, true, cloneSnapshot(s.snapshot)
}

func (s *Store) SetUpdateState(state protocol.UpdateState) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := state
	s.snapshot.Update = &copied
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

func (s *Store) SelectPet(input protocol.PetSelect) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.SelectedPetID = clamp(input.PetID, 160)
	s.savePersistedStateLocked()
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

func (s *Store) SetInstalledPets(input protocol.InstalledPetsSet) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	pets := make([]protocol.PetRef, 0, len(input.Pets))
	for _, pet := range input.Pets {
		if strings.TrimSpace(pet.ID) == "" {
			continue
		}
		pets = append(pets, sanitizePet(pet))
	}
	// Duplicate packages of the same pet (legacy non-canonical slugs, old
	// import locations) collapse here so every client sees one row per pet.
	pets = catalog.DedupeInstalled(pets)
	sort.Slice(pets, func(i, j int) bool {
		return strings.ToLower(pets[i].DisplayName) < strings.ToLower(pets[j].DisplayName)
	})
	s.snapshot.InstalledPets = pets
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

func (s *Store) SetCatalog(input protocol.CatalogCache) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider := clamp(input.Provider, 80)
	if provider == "" {
		provider = "unknown"
	}
	pets := make([]protocol.PetRef, 0, len(input.Pets))
	for _, pet := range input.Pets {
		if strings.TrimSpace(pet.ID) != "" {
			pets = append(pets, sanitizePet(pet))
		}
	}
	input.Provider = provider
	input.Pets = pets
	input.Error = safeSummary(input.Error)
	if input.UpdatedAt.IsZero() {
		input.UpdatedAt = s.now().UTC()
	}
	if s.snapshot.Catalogs == nil {
		s.snapshot.Catalogs = map[string]protocol.CatalogCache{}
	}
	s.snapshot.Catalogs[provider] = input
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

func (s *Store) ExpireApproval(id string) (bool, protocol.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.snapshot.PendingApprovals[:0]
	found := false
	for _, pending := range s.snapshot.PendingApprovals {
		if pending.ID == id {
			found = true
			continue
		}
		next = append(next, pending)
	}
	if !found {
		return false, cloneSnapshot(s.snapshot)
	}
	s.snapshot.PendingApprovals = next
	if ch := s.waiters[id]; ch != nil {
		ch <- protocol.ApprovalDecision{ApprovalID: id, Decision: protocol.ApprovalExpired, Reason: "approval timed out"}
		close(ch)
		delete(s.waiters, id)
	}
	s.finishMutationLocked(s.now().UTC())
	return true, cloneSnapshot(s.snapshot)
}

func (s *Store) ensureSessionLocked(id string, ts time.Time) *protocol.Session {
	id = normalizedSessionID(id)
	for i := range s.snapshot.Sessions {
		if s.snapshot.Sessions[i].ID == id {
			return &s.snapshot.Sessions[i]
		}
	}
	s.snapshot.Sessions = append(s.snapshot.Sessions, protocol.Session{
		ID:        id,
		Status:    protocol.SessionIdle,
		StartedAt: ts,
		UpdatedAt: ts,
	})
	return &s.snapshot.Sessions[len(s.snapshot.Sessions)-1]
}

func (s *Store) isRemovedSessionLocked(id string) bool {
	_, removed := s.removed[normalizedSessionID(id)]
	return removed
}

func (s *Store) finishMutationLocked(ts time.Time) {
	sort.Slice(s.snapshot.Sessions, func(i, j int) bool {
		if s.snapshot.Sessions[i].UpdatedAt.Equal(s.snapshot.Sessions[j].UpdatedAt) {
			return s.snapshot.Sessions[i].ID < s.snapshot.Sessions[j].ID
		}
		return s.snapshot.Sessions[i].UpdatedAt.After(s.snapshot.Sessions[j].UpdatedAt)
	})
	sort.Slice(s.snapshot.PendingApprovals, func(i, j int) bool {
		return s.snapshot.PendingApprovals[i].CreatedAt.Before(s.snapshot.PendingApprovals[j].CreatedAt)
	})
	s.snapshot.Attention = deriveAttention(s.snapshot.Sessions, s.snapshot.PendingApprovals)
	s.snapshot.Presentation = overlay.BuildPresentation(s.snapshot)
	s.applyBrainLocked(ts)
	s.snapshot.UpdatedAt = ts
}

func deriveAttention(sessions []protocol.Session, approvals []protocol.PendingApproval) protocol.AttentionState {
	for _, approval := range approvals {
		if approval.State == protocol.ApprovalPending {
			return protocol.AttentionApprovalRequired
		}
	}

	best := protocol.AttentionIdle
	bestRank := attentionRank(best)
	for _, session := range sessions {
		attention := attentionForSession(session.Status)
		rank := attentionRank(attention)
		if rank > bestRank {
			best = attention
			bestRank = rank
		}
	}
	return best
}

func attentionForSession(status protocol.SessionStatus) protocol.AttentionState {
	switch protocol.NormalizeStatus(status) {
	case protocol.SessionFailed, protocol.SessionDisconnected:
		return protocol.AttentionFailed
	case protocol.SessionDone:
		return protocol.AttentionDone
	case protocol.SessionRunning:
		return protocol.AttentionRunning
	case protocol.SessionThinking:
		return protocol.AttentionThinking
	default:
		return protocol.AttentionIdle
	}
}

func attentionRank(attention protocol.AttentionState) int {
	switch attention {
	case protocol.AttentionApprovalRequired:
		return 60
	case protocol.AttentionFailed:
		return 50
	case protocol.AttentionDone:
		return 40
	case protocol.AttentionRunning:
		return 30
	case protocol.AttentionThinking:
		return 20
	default:
		return 10
	}
}

func cloneSnapshot(snapshot protocol.Snapshot) protocol.Snapshot {
	out := snapshot
	out.Sessions = append([]protocol.Session{}, snapshot.Sessions...)
	for i := range out.Sessions {
		out.Sessions[i].Tools = append([]protocol.ToolRun(nil), snapshot.Sessions[i].Tools...)
	}
	out.PendingApprovals = append([]protocol.PendingApproval{}, snapshot.PendingApprovals...)
	out.InstalledPets = append([]protocol.PetRef{}, snapshot.InstalledPets...)
	out.Presentation.ActiveSessionIDs = append([]string{}, snapshot.Presentation.ActiveSessionIDs...)
	if snapshot.Update != nil {
		update := *snapshot.Update
		out.Update = &update
	}
	out.Catalogs = make(map[string]protocol.CatalogCache, len(snapshot.Catalogs))
	for key, catalog := range snapshot.Catalogs {
		catalog.Pets = append([]protocol.PetRef{}, catalog.Pets...)
		out.Catalogs[key] = catalog
	}
	return out
}

func sanitizePet(pet protocol.PetRef) protocol.PetRef {
	frameWidth := pet.FrameWidth
	if frameWidth < 0 {
		frameWidth = 0
	}
	frameHeight := pet.FrameHeight
	if frameHeight < 0 {
		frameHeight = 0
	}
	return protocol.PetRef{
		// IDs embed the package path ("source:slug:dir"), so the ceiling
		// must clear source+slug+path without truncating.
		ID:              clamp(pet.ID, 700),
		Slug:            clamp(pet.Slug, 80),
		DisplayName:     clamp(pet.DisplayName, 160),
		Description:     clamp(pet.Description, 500),
		Kind:            clamp(pet.Kind, 80),
		Source:          clamp(pet.Source, 80),
		Path:            clamp(pet.Path, 500),
		SpritesheetPath: clamp(pet.SpritesheetPath, 200),
		FrameWidth:      frameWidth,
		FrameHeight:     frameHeight,
		License:         clamp(pet.License, 120),
		Attribution:     clamp(pet.Attribution, 240),
	}
}

func normalizedSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "default"
	}
	return id
}

func safeSummary(value string) string {
	return clamp(strings.Join(strings.Fields(value), " "), maxSafeSummaryLength)
}

func clamp(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
