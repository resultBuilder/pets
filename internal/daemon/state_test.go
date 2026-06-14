package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-pets/internal/protocol"
)

func TestSelectedPetPersistsAcrossStores(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "nested", "daemon-state.json")

	first := NewStoreWithStateFile(stateFile)
	first.SelectPet(protocol.PetSelect{PetID: "petdex:boba"})

	second := NewStoreWithStateFile(stateFile)
	if got := second.Snapshot().SelectedPetID; got != "petdex:boba" {
		t.Fatalf("restored selected pet = %q, want petdex:boba", got)
	}

	if err := os.WriteFile(stateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	third := NewStoreWithStateFile(stateFile)
	if got := third.Snapshot().SelectedPetID; got != "" {
		t.Fatalf("corrupt state file should reset selection, got %q", got)
	}

	fourth := NewStoreWithStateFile("")
	fourth.SelectPet(protocol.PetSelect{PetID: "petdex:cat"})
	fifth := NewStoreWithStateFile("")
	if got := fifth.Snapshot().SelectedPetID; got != "" {
		t.Fatalf("empty path should disable persistence, got %q", got)
	}
}

func TestAttentionPriority(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store := NewStoreWithClock(func() time.Time { return now })

	store.UpsertSession(protocol.SessionUpsert{SessionID: "thinking", Status: protocol.SessionThinking})
	if got := store.Snapshot().Attention; got != protocol.AttentionThinking {
		t.Fatalf("attention = %s, want thinking", got)
	}

	store.UpsertSession(protocol.SessionUpsert{SessionID: "running", Status: protocol.SessionRunning})
	if got := store.Snapshot().Attention; got != protocol.AttentionRunning {
		t.Fatalf("attention = %s, want running", got)
	}

	store.UpsertSession(protocol.SessionUpsert{SessionID: "done", Status: protocol.SessionDone})
	if got := store.Snapshot().Attention; got != protocol.AttentionDone {
		t.Fatalf("attention = %s, want done", got)
	}

	store.UpsertSession(protocol.SessionUpsert{SessionID: "failed", Status: protocol.SessionFailed})
	if got := store.Snapshot().Attention; got != protocol.AttentionFailed {
		t.Fatalf("attention = %s, want failed", got)
	}

	store.AddApproval(protocol.ApprovalRequest{ApprovalID: "a1", SessionID: "running", ToolName: "bash"})
	if got := store.Snapshot().Attention; got != protocol.AttentionApprovalRequired {
		t.Fatalf("attention = %s, want approval_required", got)
	}
}

func TestApprovalBrokerDoesNotStoreRawPayload(t *testing.T) {
	store := NewStore()
	store.AddApproval(protocol.ApprovalRequest{
		ApprovalID:     "a1",
		SessionID:      "s1",
		ToolCallID:     "t1",
		ToolName:       "bash",
		CommandSummary: "  rm -rf build   ",
		Risk:           "destructive",
	})

	snapshot := store.Snapshot()
	if len(snapshot.PendingApprovals) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(snapshot.PendingApprovals))
	}
	pending := snapshot.PendingApprovals[0]
	if pending.CommandSummary != "rm -rf build" {
		t.Fatalf("summary = %q", pending.CommandSummary)
	}
	if pending.State != protocol.ApprovalPending {
		t.Fatalf("state = %s, want pending", pending.State)
	}

	decision, ok, snapshot := store.ResolveApproval(protocol.ApprovalDecision{
		ApprovalID: "a1",
		Decision:   protocol.ApprovalApproved,
		Reason:     "ok",
	})
	if !ok {
		t.Fatal("expected approval to resolve")
	}
	if decision.Decision != protocol.ApprovalApproved {
		t.Fatalf("decision = %s", decision.Decision)
	}
	if len(snapshot.PendingApprovals) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(snapshot.PendingApprovals))
	}
}

func TestRemoveSessionExpiresPendingApprovals(t *testing.T) {
	store := NewStore()
	_, decisions, _ := store.AddApproval(protocol.ApprovalRequest{
		ApprovalID: "a1",
		SessionID:  "s1",
		ToolName:   "bash",
	})
	store.UpsertSession(protocol.SessionUpsert{SessionID: "s1", Status: protocol.SessionRunning})

	snapshot := store.RemoveSession(protocol.SessionRemove{SessionID: "s1"})
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want none", snapshot.Sessions)
	}
	if len(snapshot.PendingApprovals) != 0 {
		t.Fatalf("pending approvals = %+v, want none", snapshot.PendingApprovals)
	}
	if snapshot.Attention != protocol.AttentionIdle {
		t.Fatalf("attention = %s, want idle", snapshot.Attention)
	}

	select {
	case decision := <-decisions:
		if decision.Decision != protocol.ApprovalExpired {
			t.Fatalf("decision = %s, want expired", decision.Decision)
		}
		if decision.Reason != "session terminated" {
			t.Fatalf("reason = %q, want session terminated", decision.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("pending approval was not released")
	}

	_, lateDecisions, snapshot := store.AddApproval(protocol.ApprovalRequest{
		ApprovalID: "late",
		SessionID:  "s1",
		ToolName:   "bash",
	})
	if len(snapshot.PendingApprovals) != 0 {
		t.Fatalf("late pending approvals = %+v, want none", snapshot.PendingApprovals)
	}
	select {
	case decision := <-lateDecisions:
		if decision.Decision != protocol.ApprovalExpired {
			t.Fatalf("late decision = %s, want expired", decision.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("late approval was not released")
	}

	snapshot = store.ToolStart(protocol.ToolUpdate{
		SessionID:   "s1",
		ToolCallID:  "late-tool",
		ToolName:    "bash",
		SafeSummary: "late tool",
	})
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("late tool recreated removed session: %+v", snapshot.Sessions)
	}

	store.UpsertSession(protocol.SessionUpsert{SessionID: "s1", Status: protocol.SessionRunning})
	store.ToolStart(protocol.ToolUpdate{
		SessionID:   "s1",
		ToolCallID:  "after-restart-tool",
		ToolName:    "bash",
		SafeSummary: "after restart",
	})
	store.AddApproval(protocol.ApprovalRequest{
		ApprovalID: "after-restart",
		SessionID:  "s1",
		ToolName:   "bash",
	})
	restarted := store.Snapshot()
	if got := len(restarted.Sessions); got != 1 {
		t.Fatalf("sessions after restart = %d, want 1", got)
	}
	if got := len(restarted.Sessions[0].Tools); got != 1 {
		t.Fatalf("tools after restart = %d, want 1", got)
	}
	if got := len(restarted.PendingApprovals); got != 1 {
		t.Fatalf("pending approvals after restart = %d, want 1", got)
	}
}

func TestToolUpdateDoesNotRestartRunningTool(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store := NewStoreWithClock(func() time.Time { return now })

	snapshot := store.ToolStart(protocol.ToolUpdate{
		SessionID:   "s1",
		ToolCallID:  "tool-1",
		ToolName:    "bash",
		SafeSummary: "bash started",
	})
	startedAt := snapshot.Sessions[0].Tools[0].StartedAt

	now = now.Add(time.Second)
	snapshot = store.ToolUpdate(protocol.ToolUpdate{
		SessionID:   "s1",
		ToolCallID:  "tool-1",
		ToolName:    "bash",
		SafeSummary: "bash running",
	})
	tool := snapshot.Sessions[0].Tools[0]
	if tool.State != protocol.ToolRunning {
		t.Fatalf("tool state = %s, want running", tool.State)
	}
	if !tool.StartedAt.Equal(startedAt) {
		t.Fatalf("tool startedAt changed on update: got %s want %s", tool.StartedAt, startedAt)
	}
	if !tool.EndedAt.IsZero() {
		t.Fatalf("tool endedAt set on update: %s", tool.EndedAt)
	}
	if tool.SafeSummary != "bash running" {
		t.Fatalf("tool summary = %q, want progress summary", tool.SafeSummary)
	}
}

func TestSnapshotJSONCollectionsAreArrays(t *testing.T) {
	store := NewStoreWithClock(func() time.Time {
		return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	})

	data, err := json.Marshal(store.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode snapshot json: %v", err)
	}

	for _, key := range []string{"sessions", "pendingApprovals", "installedPets"} {
		if _, ok := object[key].([]any); !ok {
			t.Fatalf("%s serialized as %T, want JSON array: %s", key, object[key], data)
		}
	}

	store.SetCatalog(protocol.CatalogCache{Provider: "petdex"})
	data, err = json.Marshal(store.Snapshot())
	if err != nil {
		t.Fatalf("marshal catalog snapshot: %v", err)
	}
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode catalog snapshot json: %v", err)
	}
	catalogs := object["catalogs"].(map[string]any)
	petdex := catalogs["petdex"].(map[string]any)
	if _, ok := petdex["pets"].([]any); !ok {
		t.Fatalf("catalog pets serialized as %T, want JSON array: %s", petdex["pets"], data)
	}
}

func TestPetSelectionInstalledPetsAndCatalogCacheState(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store := NewStoreWithClock(func() time.Time { return now })

	store.SetInstalledPets(protocol.InstalledPetsSet{Pets: []protocol.PetRef{
		{
			ID:          "pet-z",
			DisplayName: "Zeta",
			Source:      "app",
			Path:        "/pets/zeta",
			License:     "MIT",
			Attribution: "Ada",
		},
		{ID: "", DisplayName: "missing id", Source: "app"},
		{
			ID:          "pet-a",
			DisplayName: "Alpha",
			Source:      "codex",
			Path:        "/pets/alpha",
			License:     "Apache-2.0",
			Attribution: "Grace",
		},
	}})
	store.SelectPet(protocol.PetSelect{PetID: "pet-a"})
	store.SetCatalog(protocol.CatalogCache{
		Provider:  "  petdex  ",
		UpdatedAt: now.Add(-time.Hour),
		Pets: []protocol.PetRef{
			{ID: "remote-1", DisplayName: "Remote One", Source: "petdex", License: "unknown", Attribution: "petdex"},
			{ID: "", DisplayName: "ignored", Source: "petdex"},
		},
		Error: "  network   unavailable  ",
	})

	snapshot := store.Snapshot()
	if snapshot.SelectedPetID != "pet-a" {
		t.Fatalf("selected pet = %q, want pet-a", snapshot.SelectedPetID)
	}
	if len(snapshot.InstalledPets) != 2 {
		t.Fatalf("installed pets = %d, want 2", len(snapshot.InstalledPets))
	}
	if snapshot.InstalledPets[0].ID != "pet-a" || snapshot.InstalledPets[1].ID != "pet-z" {
		t.Fatalf("installed pets not sorted by display name: %+v", snapshot.InstalledPets)
	}
	if snapshot.InstalledPets[0].License != "Apache-2.0" || snapshot.InstalledPets[0].Attribution != "Grace" {
		t.Fatalf("installed pet license/attribution not preserved: %+v", snapshot.InstalledPets[0])
	}

	catalog, ok := snapshot.Catalogs["petdex"]
	if !ok {
		t.Fatalf("petdex catalog cache missing: %+v", snapshot.Catalogs)
	}
	if catalog.Error != "network unavailable" {
		t.Fatalf("catalog error = %q, want safe collapsed summary", catalog.Error)
	}
	if !catalog.UpdatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("catalog updatedAt = %s, want caller timestamp", catalog.UpdatedAt)
	}
	if len(catalog.Pets) != 1 || catalog.Pets[0].ID != "remote-1" {
		t.Fatalf("catalog pets = %+v, want one sanitized pet", catalog.Pets)
	}

	snapshot.InstalledPets[0].DisplayName = "mutated"
	catalog.Pets[0].DisplayName = "mutated"
	snapshot.Catalogs["petdex"] = catalog
	next := store.Snapshot()
	if next.InstalledPets[0].DisplayName != "Alpha" {
		t.Fatalf("snapshot clone leaked installed pet mutation: %+v", next.InstalledPets[0])
	}
	if next.Catalogs["petdex"].Pets[0].DisplayName != "Remote One" {
		t.Fatalf("snapshot clone leaked catalog pet mutation: %+v", next.Catalogs["petdex"].Pets[0])
	}
}
