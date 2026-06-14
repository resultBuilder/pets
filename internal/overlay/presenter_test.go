package overlay

import (
	"testing"

	"codex-pets/internal/protocol"
)

func TestPresentMapsAttentionPriorityStates(t *testing.T) {
	cases := []struct {
		name      string
		attention protocol.AttentionState
		status    protocol.SessionStatus
		approval  bool
		wantState string
	}{
		{"approval", protocol.AttentionApprovalRequired, protocol.SessionRunning, true, "waiting"},
		{"failed", protocol.AttentionFailed, protocol.SessionFailed, false, "failed"},
		{"done", protocol.AttentionDone, protocol.SessionDone, false, "waving"},
		{"running", protocol.AttentionRunning, protocol.SessionRunning, false, "running"},
		{"thinking", protocol.AttentionThinking, protocol.SessionThinking, false, "review"},
		{"idle", protocol.AttentionIdle, protocol.SessionIdle, false, "idle"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := protocol.Snapshot{
				Attention: tc.attention,
				Sessions: []protocol.Session{{
					ID:          "s1",
					Status:      tc.status,
					SafeSummary: "running tests",
				}},
			}
			if tc.approval {
				snapshot.PendingApprovals = []protocol.PendingApproval{{
					ID:             "a1",
					SessionID:      "s1",
					ToolName:       "bash",
					CommandSummary: "git push origin main",
					State:          protocol.ApprovalPending,
				}}
			}
			got := Present(snapshot)
			if got.StateID != tc.wantState {
				t.Fatalf("state = %q, want %q", got.StateID, tc.wantState)
			}
		})
	}
}

func TestPresentSelectsPetAndSafeApprovalBubble(t *testing.T) {
	got := Present(protocol.Snapshot{
		Attention:     protocol.AttentionApprovalRequired,
		SelectedPetID: "pet-2",
		InstalledPets: []protocol.PetRef{
			{ID: "pet-1", Path: "/pets/one"},
			{ID: "pet-2", Path: "/pets/two"},
		},
		PendingApprovals: []protocol.PendingApproval{{
			ID:             "a1",
			SessionID:      "s1",
			ToolName:       "bash",
			CommandSummary: "git push origin main\ncat secret",
			State:          protocol.ApprovalPending,
		}},
	})

	if got.SelectedPetID != "pet-2" || got.SelectedPetPath != "/pets/two" {
		t.Fatalf("selected pet = %q %q", got.SelectedPetID, got.SelectedPetPath)
	}
	if got.Bubble != "Approval needed: git push origin main cat secret" {
		t.Fatalf("bubble = %q", got.Bubble)
	}
}
