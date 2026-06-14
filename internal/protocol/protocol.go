package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const Version = 1

const (
	KindRequest  = "request"
	KindResponse = "response"
	KindEvent    = "event"
)

const (
	MethodHello            = "hello"
	MethodSnapshotGet      = "snapshot.get"
	MethodStateSubscribe   = "state.subscribe"
	MethodSessionUpsert    = "session.upsert"
	MethodSessionRemove    = "session.remove"
	MethodSessionAttach    = "session.attach"
	MethodToolStart        = "tool.start"
	MethodToolUpdate       = "tool.update"
	MethodToolEnd          = "tool.end"
	MethodApprovalRequest  = "approval.request"
	MethodApprovalRespond  = "approval.respond"
	MethodPetSelect        = "pet.select"
	MethodInstalledPetsSet = "pets.installed.set"
	MethodPetsRefresh      = "pets.refresh"
	MethodPetsBrowserList  = "pets.browser.list"
	MethodPetImport        = "pet.import"
	MethodPetUninstall     = "pet.uninstall"
	MethodCatalogSet       = "catalog.cache.set"

	MethodPiExtensionStatus    = "pi.extension.status"
	MethodPiExtensionInstall   = "pi.extension.install"
	MethodPiExtensionUninstall = "pi.extension.uninstall"

	MethodUpdateCheck   = "update.check"
	MethodUpdateApply   = "update.apply"
	MethodUpdateDismiss = "update.dismiss"

	MethodOverlayInteraction = "overlay.interaction"
	MethodOverlaySettingsSet = "overlay.settings.set"
	MethodMurmursMute        = "murmurs.mute"

	EventSnapshot = "state.snapshot"
)

const (
	ClientPiExtension = "pi-extension"
	ClientOverlay     = "overlay"
	ClientBrowser     = "browser"
	ClientMock        = "mock"
)

type Message struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewRequest(id string, method string, payload any) (Message, error) {
	return newMessage(KindRequest, id, method, payload, nil)
}

func NewResponse(id string, method string, payload any) (Message, error) {
	return newMessage(KindResponse, id, method, payload, nil)
}

func NewErrorResponse(id string, method string, code string, message string) Message {
	return Message{
		Version: Version,
		Kind:    KindResponse,
		ID:      id,
		Method:  method,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
}

func NewEvent(method string, payload any) (Message, error) {
	return newMessage(KindEvent, "", method, payload, nil)
}

func newMessage(kind string, id string, method string, payload any, msgErr *Error) (Message, error) {
	raw, err := MarshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Version: Version,
		Kind:    kind,
		ID:      id,
		Method:  method,
		Payload: raw,
		Error:   msgErr,
	}, nil
}

func MarshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func DecodePayload[T any](msg Message) (T, error) {
	var out T
	if len(msg.Payload) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(msg.Payload, &out); err != nil {
		return out, err
	}
	return out, nil
}

func EncodeLine(msg Message) ([]byte, error) {
	if err := ValidateMessage(msg); err != nil {
		return nil, err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}

func DecodeLine(line []byte) (Message, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Message{}, errors.New("empty protocol line")
	}
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return Message{}, err
	}
	if err := ValidateMessage(msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func ValidateMessage(msg Message) error {
	if msg.Version != Version {
		return fmt.Errorf("unsupported protocol version %d", msg.Version)
	}
	switch msg.Kind {
	case KindRequest:
		if strings.TrimSpace(msg.ID) == "" {
			return errors.New("request id is required")
		}
	case KindResponse:
		if strings.TrimSpace(msg.ID) == "" {
			return errors.New("response id is required")
		}
	case KindEvent:
	default:
		return fmt.Errorf("unsupported message kind %q", msg.Kind)
	}
	if strings.TrimSpace(msg.Method) == "" {
		return errors.New("method is required")
	}
	return nil
}

type Hello struct {
	Client string `json:"client"`
	Name   string `json:"name,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

type SessionStatus string

const (
	SessionIdle         SessionStatus = "idle"
	SessionThinking     SessionStatus = "thinking"
	SessionRunning      SessionStatus = "running"
	SessionDone         SessionStatus = "done"
	SessionFailed       SessionStatus = "failed"
	SessionDisconnected SessionStatus = "disconnected"
)

type AttentionState string

const (
	AttentionIdle             AttentionState = "idle"
	AttentionThinking         AttentionState = "thinking"
	AttentionRunning          AttentionState = "running"
	AttentionDone             AttentionState = "done"
	AttentionFailed           AttentionState = "failed"
	AttentionApprovalRequired AttentionState = "approval_required"
)

type ToolState string

const (
	ToolRunning ToolState = "running"
	ToolDone    ToolState = "done"
	ToolFailed  ToolState = "failed"
)

type Session struct {
	ID          string        `json:"id"`
	CWD         string        `json:"cwd,omitempty"`
	Title       string        `json:"title,omitempty"`
	Status      SessionStatus `json:"status"`
	SafeSummary string        `json:"safeSummary,omitempty"`
	Tools       []ToolRun     `json:"tools,omitempty"`
	StartedAt   time.Time     `json:"startedAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

type ToolRun struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"sessionId"`
	Name        string    `json:"name"`
	State       ToolState `json:"state"`
	SafeSummary string    `json:"safeSummary,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt,omitempty"`
}

type SessionUpsert struct {
	SessionID   string        `json:"sessionId"`
	CWD         string        `json:"cwd,omitempty"`
	Title       string        `json:"title,omitempty"`
	Status      SessionStatus `json:"status"`
	SafeSummary string        `json:"safeSummary,omitempty"`
}

type SessionRemove struct {
	SessionID string `json:"sessionId"`
}

// SessionAttach binds a session's lifetime to the requesting connection:
// when that connection closes without an explicit session.remove, the daemon
// removes the session itself. This keeps crashed or killed clients from
// leaving stale sessions behind.
type SessionAttach struct {
	SessionID string `json:"sessionId"`
}

type ToolUpdate struct {
	SessionID   string    `json:"sessionId"`
	ToolCallID  string    `json:"toolCallId"`
	ToolName    string    `json:"toolName"`
	State       ToolState `json:"state,omitempty"`
	SafeSummary string    `json:"safeSummary,omitempty"`
}

type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
	ApprovalExpired  ApprovalState = "expired"
)

type ApprovalRequest struct {
	ApprovalID     string `json:"approvalId"`
	SessionID      string `json:"sessionId"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	ToolName       string `json:"toolName"`
	CommandSummary string `json:"commandSummary,omitempty"`
	Risk           string `json:"risk,omitempty"`
	TimeoutMillis  int    `json:"timeoutMillis,omitempty"`
}

type PendingApproval struct {
	ID             string        `json:"id"`
	SessionID      string        `json:"sessionId"`
	ToolCallID     string        `json:"toolCallId,omitempty"`
	ToolName       string        `json:"toolName"`
	CommandSummary string        `json:"commandSummary,omitempty"`
	Risk           string        `json:"risk,omitempty"`
	State          ApprovalState `json:"state"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

type ApprovalDecision struct {
	ApprovalID string        `json:"approvalId"`
	Decision   ApprovalState `json:"decision"`
	Reason     string        `json:"reason,omitempty"`
}

// PetRef carries everything a host needs to render and present a pet, so
// native code never parses pet.json itself.
type PetRef struct {
	ID              string `json:"id"`
	Slug            string `json:"slug,omitempty"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Source          string `json:"source"`
	Path            string `json:"path,omitempty"`
	SpritesheetPath string `json:"spritesheetPath,omitempty"`
	FrameWidth      int    `json:"frameWidth,omitempty"`
	FrameHeight     int    `json:"frameHeight,omitempty"`
	License         string `json:"license,omitempty"`
	Attribution     string `json:"attribution,omitempty"`
}

type InstalledPetsSet struct {
	Pets []PetRef `json:"pets"`
}

type PetSelect struct {
	PetID string `json:"petId"`
}

// PetImport carries a pet package into the daemon, which owns validation
// and storage. Local packages travel as a directory path; downloaded ones
// as raw pet.json plus a base64 spritesheet.
type PetImport struct {
	Directory         string `json:"directory,omitempty"`
	PetJSON           string `json:"petJson,omitempty"`
	SpritesheetBase64 string `json:"spritesheetBase64,omitempty"`
	SpritesheetExt    string `json:"spritesheetExt,omitempty"`
	Source            string `json:"source,omitempty"`
}

type PetUninstall struct {
	PetID string `json:"petId"`
}

// PetOpResult is the response payload for pet.import and pet.uninstall.
type PetOpResult struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message,omitempty"`
	Pet     *PetRef `json:"pet,omitempty"`
}

// BrowserPetRow is one entry of the merged installed+catalog picker list
// served by pets.browser.list; pages render it without further logic.
type BrowserPetRow struct {
	PetRef
	Installed    bool `json:"installed"`
	CanUninstall bool `json:"canUninstall"`
}

type BrowserPetList struct {
	Rows []BrowserPetRow `json:"rows"`
}

// PiExtensionResult is the response payload for pi.extension.* methods.
type PiExtensionResult struct {
	OK               bool   `json:"ok"`
	Message          string `json:"message,omitempty"`
	Available        bool   `json:"available"`
	Installed        bool   `json:"installed"`
	NeedsUpdate      bool   `json:"needsUpdate"`
	Path             string `json:"path,omitempty"`
	SourceVersion    string `json:"sourceVersion,omitempty"`
	InstalledVersion string `json:"installedVersion,omitempty"`
}

type CatalogCache struct {
	Provider  string    `json:"provider"`
	UpdatedAt time.Time `json:"updatedAt"`
	Pets      []PetRef  `json:"pets"`
	Error     string    `json:"error,omitempty"`
}

// Overlay interaction kinds for OverlayInteraction.Type.
const (
	InteractionClick        = "click"
	InteractionDrag         = "drag"
	InteractionMouseNear    = "mouseNear"
	InteractionUserReturned = "userReturned"
)

// OverlayInteraction reports user input on the pet. The daemon's brain only
// picks the murmur line (shared phrase book and cooldown history); the pose
// reaction stays local to the renderer, which is why the response is a
// direct InteractionResult instead of a snapshot broadcast.
type OverlayInteraction struct {
	Type   string `json:"type"`
	Clicks int    `json:"clicks,omitempty"`
}

// InteractionResult is the daemon's reply to overlay.interaction. Murmur is
// the line to show (empty = stay quiet); StateID/DurationSeconds are pose
// hints for renderers without a local brain.
type InteractionResult struct {
	Murmur          string  `json:"murmur,omitempty"`
	StateID         string  `json:"stateId,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
}

// OverlaySettings tunes the pet's temperament; empty fields stay unchanged.
type OverlaySettings struct {
	AttentionMode string `json:"attentionMode,omitempty"`
	BubbleMode    string `json:"bubbleMode,omitempty"`
	ReduceMotion  *bool  `json:"reduceMotion,omitempty"`
}

// MurmursMute silences murmur lines until local midnight (today) or for a
// fixed number of seconds (a dismissed bubble quiets the pet for a while).
type MurmursMute struct {
	Today   bool    `json:"today,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

// Presentation is the canonical overlay interpretation of a snapshot,
// computed by the daemon so every overlay renders identical pet behavior.
type Presentation struct {
	StateID          string   `json:"stateId"`
	Bubble           string   `json:"bubble,omitempty"`
	AutoClearSeconds float64  `json:"autoClearSeconds,omitempty"`
	ActiveSessionIDs []string `json:"activeSessionIds"`
}

// UpdateState reports the shared self-update pipeline driven by the daemon.
// Hosts trigger update.check/update.apply and restart themselves when Stage
// reaches "restartPending".
type UpdateState struct {
	Available     bool   `json:"available"`
	CommitsBehind int    `json:"commitsBehind,omitempty"`
	Stage         string `json:"stage,omitempty"`
	Message       string `json:"message,omitempty"`
}

type Snapshot struct {
	Attention        AttentionState          `json:"attention"`
	Sessions         []Session               `json:"sessions"`
	PendingApprovals []PendingApproval       `json:"pendingApprovals"`
	SelectedPetID    string                  `json:"selectedPetId,omitempty"`
	InstalledPets    []PetRef                `json:"installedPets"`
	Catalogs         map[string]CatalogCache `json:"catalogs"`
	Presentation     Presentation            `json:"presentation"`
	Update           *UpdateState            `json:"update,omitempty"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

func NormalizeStatus(status SessionStatus) SessionStatus {
	switch status {
	case SessionIdle, SessionThinking, SessionRunning, SessionDone, SessionFailed, SessionDisconnected:
		return status
	default:
		return SessionIdle
	}
}

func NormalizeToolState(state ToolState) ToolState {
	switch state {
	case ToolRunning, ToolDone, ToolFailed:
		return state
	default:
		return ToolRunning
	}
}

func NormalizeDecision(state ApprovalState) (ApprovalState, bool) {
	switch state {
	case ApprovalApproved, ApprovalDenied:
		return state, true
	default:
		return "", false
	}
}
