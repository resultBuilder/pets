package overlay

import (
	"strings"

	"codex-pets/internal/protocol"
)

// Presentation is the renderer-facing view used by Go overlay clients. It
// extends the wire presentation with the resolved selected pet.
type Presentation struct {
	StateID           string
	Bubble            string
	AutoClearSeconds  float64
	PendingApprovalID string
	SelectedPetID     string
	SelectedPetPath   string
	ActiveSessionIDs  []string
}

// BuildPresentation derives the canonical wire presentation for a snapshot.
// The daemon embeds its result into every published snapshot; it is the
// single source of truth for how overlays interpret daemon state.
func BuildPresentation(snapshot protocol.Snapshot) protocol.Presentation {
	presentation := protocol.Presentation{StateID: "idle"}

	switch snapshot.Attention {
	case protocol.AttentionApprovalRequired:
		presentation.StateID = "waiting"
		presentation.Bubble = approvalBubble(snapshot.PendingApprovals)
	case protocol.AttentionFailed:
		presentation.StateID = "failed"
		presentation.Bubble = sessionBubble("Pi failed", matchingSessions(snapshot.Sessions, protocol.SessionFailed, protocol.SessionDisconnected))
		presentation.AutoClearSeconds = 8
	case protocol.AttentionDone:
		presentation.StateID = "waving"
		presentation.Bubble = sessionBubble("Pi done", matchingSessions(snapshot.Sessions, protocol.SessionDone))
		presentation.AutoClearSeconds = 6
	case protocol.AttentionRunning:
		presentation.StateID = "running"
		presentation.Bubble = sessionBubble("Pi running", matchingSessions(snapshot.Sessions, protocol.SessionRunning))
	case protocol.AttentionThinking:
		presentation.StateID = "review"
		presentation.Bubble = sessionBubble("Pi thinking", matchingSessions(snapshot.Sessions, protocol.SessionThinking))
	default:
		presentation.StateID = "idle"
	}

	presentation.ActiveSessionIDs = activeSessionIDs(snapshot.Sessions)
	return presentation
}

func Present(snapshot protocol.Snapshot) Presentation {
	wire := snapshot.Presentation
	if wire.StateID == "" {
		wire = BuildPresentation(snapshot)
	}
	selected := selectedPet(snapshot)
	return Presentation{
		StateID:           wire.StateID,
		Bubble:            wire.Bubble,
		AutoClearSeconds:  wire.AutoClearSeconds,
		PendingApprovalID: pendingApprovalID(snapshot.PendingApprovals),
		SelectedPetID:     selected.ID,
		SelectedPetPath:   selected.Path,
		ActiveSessionIDs:  wire.ActiveSessionIDs,
	}
}

func pendingApprovalID(approvals []protocol.PendingApproval) string {
	for _, approval := range approvals {
		if approval.State == protocol.ApprovalPending {
			return approval.ID
		}
	}
	return ""
}

func selectedPet(snapshot protocol.Snapshot) protocol.PetRef {
	if snapshot.SelectedPetID != "" {
		for _, pet := range snapshot.InstalledPets {
			if pet.ID == snapshot.SelectedPetID {
				return pet
			}
		}
	}
	if len(snapshot.InstalledPets) > 0 {
		return snapshot.InstalledPets[0]
	}
	return protocol.PetRef{}
}

func approvalBubble(approvals []protocol.PendingApproval) string {
	for _, approval := range approvals {
		if approval.State != protocol.ApprovalPending {
			continue
		}
		summary := safeText(firstNonEmpty(approval.CommandSummary, approval.ToolName), 70)
		if summary == "" {
			return "Approval needed"
		}
		return "Approval needed: " + summary
	}
	return "Approval needed"
}

func sessionBubble(prefix string, sessions []protocol.Session) string {
	if len(sessions) == 0 {
		return ""
	}
	if len(sessions) > 1 {
		return prefix + ": " + intString(len(sessions)) + " sessions"
	}
	session := sessions[0]
	summary := safeText(firstNonEmpty(session.SafeSummary, session.Title, session.CWD, session.ID), 70)
	if summary == "" {
		return prefix
	}
	return prefix + ": " + summary
}

func matchingSessions(sessions []protocol.Session, statuses ...protocol.SessionStatus) []protocol.Session {
	wanted := map[protocol.SessionStatus]struct{}{}
	for _, status := range statuses {
		wanted[status] = struct{}{}
	}
	var out []protocol.Session
	for _, session := range sessions {
		if _, ok := wanted[session.Status]; ok {
			out = append(out, session)
		}
	}
	return out
}

func activeSessionIDs(sessions []protocol.Session) []string {
	ids := []string{}
	for _, session := range sessions {
		switch session.Status {
		case protocol.SessionThinking, protocol.SessionRunning:
			ids = append(ids, session.ID)
		}
	}
	return ids
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func safeText(value string, max int) string {
	collapsed := strings.Join(strings.Fields(value), " ")
	if len(collapsed) <= max {
		return collapsed
	}
	return collapsed[:max]
}

func intString(value int) string {
	switch value {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	case 4:
		return "4"
	case 5:
		return "5"
	default:
		return "many"
	}
}
