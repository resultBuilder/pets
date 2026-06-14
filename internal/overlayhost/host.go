package overlayhost

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"codex-pets/internal/overlay"
	"codex-pets/internal/protocol"
	"codex-pets/internal/render"
)

// Options configures host-level actions that are outside the overlay renderer.
type Options struct {
	OnPetBrowserRequested func()
}

type playbackMode int

const (
	playbackStatic playbackMode = iota
	playbackPlayOnce
	playbackLoop
)

type animationKey struct {
	stateID  string
	playback playbackMode
}

func playbackForState(stateID string, transient bool) playbackMode {
	switch stateID {
	case "idle":
		if transient {
			return playbackPlayOnce
		}
		return playbackStatic
	case "waving", "jumping", "failed", "waiting", "review":
		return playbackPlayOnce
	default:
		return playbackLoop
	}
}

// Run drives a pet overlay on any Backend: it subscribes to daemon
// snapshots, composes frames with the shared renderer, paces animations,
// relays interactions, and handles the update pill. Backends only translate
// native windowing events and blit frames.
func Run(ctx context.Context, socketPath string, backend Backend, userScale float64) error {
	return RunWithOptions(ctx, socketPath, backend, userScale, Options{})
}

// RunWithOptions is Run plus host callbacks for actions such as opening the
// Petdex browser.
func RunWithOptions(ctx context.Context, socketPath string, backend Backend, userScale float64, options Options) error {
	renderScale, err := backend.Open(userScale)
	if err != nil {
		return err
	}
	defer backend.Close()
	renderer := render.NewRenderer(renderScale)

	snapshots := make(chan protocol.Snapshot, 8)
	go subscribeSnapshots(ctx, socketPath, snapshots)

	presentation := overlay.Presentation{StateID: "idle", Bubble: "Waiting for Pi Pet daemon"}
	var updateState *protocol.UpdateState
	var regions render.Regions
	frameIndex := 0
	playOnceDone := false
	relaunchScheduled := false
	requestedFirstCheck := false
	regionsApplied := false
	dragActive := false
	dragMoved := false
	dragState := "running-right"
	localPose := ""
	var localPoseUntil time.Time
	interactionResults := make(chan protocol.InteractionResult, 4)

	currentAnimation := func() animationKey {
		if dragActive {
			return animationKey{stateID: dragState, playback: playbackLoop}
		}
		if localPose != "" && time.Now().Before(localPoseUntil) {
			return animationKey{stateID: localPose, playback: playbackForState(localPose, true)}
		}
		return animationKey{stateID: presentation.StateID, playback: playbackForState(presentation.StateID, false)}
	}
	currentState := func() string {
		return currentAnimation().stateID
	}

	frameTimer := time.NewTimer(render.StateByID(currentState()).FrameDuration)
	defer frameTimer.Stop()
	eventTicker := time.NewTicker(16 * time.Millisecond)
	defer eventTicker.Stop()
	updateTicker := time.NewTicker(5 * time.Minute)
	defer updateTicker.Stop()

	var bubbleClearTimer *time.Timer
	var bubbleClearCh <-chan time.Time
	cancelBubbleClear := func() {
		if bubbleClearTimer != nil {
			bubbleClearTimer.Stop()
			bubbleClearTimer = nil
		}
		bubbleClearCh = nil
	}
	scheduleBubbleClear := func(seconds float64) {
		cancelBubbleClear()
		bubbleClearTimer = time.NewTimer(time.Duration(seconds * float64(time.Second)))
		bubbleClearCh = bubbleClearTimer.C
	}
	defer cancelBubbleClear()

	renderFrame := func() {
		index := frameIndex
		if currentAnimation().playback == playbackStatic {
			index = 0
		}
		frame, next := renderer.Compose(render.Input{
			StateID:           currentState(),
			Bubble:            presentation.Bubble,
			PendingApprovalID: presentation.PendingApprovalID,
			ActiveSessions:    len(presentation.ActiveSessionIDs),
			Update:            updateState,
			FrameIndex:        index,
		})
		backend.Present(frame)
		if !regionsApplied || !regions.Equal(next) {
			regionsApplied = true
			regions = next
			backend.SetRegions(next)
		}
	}

	resetAnimation := func(previous animationKey) {
		if currentAnimation() == previous {
			return
		}
		frameIndex = 0
		playOnceDone = false
		if !frameTimer.Stop() {
			select {
			case <-frameTimer.C:
			default:
			}
		}
		frameTimer.Reset(render.StateByID(currentState()).FrameDuration)
	}

	requestInteractionAsync := func(interaction protocol.OverlayInteraction) {
		go func() {
			result := requestInteraction(socketPath, interaction)
			if result.Murmur == "" && result.StateID == "" {
				return
			}
			select {
			case interactionResults <- result:
			default:
			}
		}()
	}

	handleEvents := func() {
		previous := currentAnimation()
		changed := false
		for _, event := range backend.Pump() {
			switch event.Kind {
			case EventBodyPress:
				dragActive = true
				dragMoved = false
				dragState = "running-right"
				changed = true
			case EventDragStep:
				if !dragActive {
					continue
				}
				dragMoved = true
				if next := dragStateForDelta(event.DX, dragState); next != dragState {
					dragState = next
					changed = true
				}
			case EventRelease:
				if !dragActive {
					continue
				}
				interaction := protocol.OverlayInteraction{Type: protocol.InteractionClick, Clicks: 1}
				if event.Moved || dragMoved {
					interaction = protocol.OverlayInteraction{Type: protocol.InteractionDrag}
				}
				requestInteractionAsync(interaction)
				dragActive = false
				changed = true
			case EventPillPress:
				if regions.UpdateAction == render.UpdateActionNone {
					continue
				}
				method := protocol.MethodUpdateApply
				if regions.UpdateAction == render.UpdateActionDismiss {
					method = protocol.MethodUpdateDismiss
				}
				go sendRequestOnce(socketPath, method)
			case EventPetBrowserRequest:
				if options.OnPetBrowserRequested != nil {
					go options.OnPetBrowserRequested()
				}
			case EventApprovalDecision:
				if event.ApprovalID == "" {
					continue
				}
				reason := "Approved from pet overlay"
				if event.ApprovalDecision == protocol.ApprovalDenied {
					reason = "Denied from pet overlay"
				}
				go sendPayloadOnce(socketPath, protocol.MethodApprovalRespond, map[string]any{
					"approvalId": event.ApprovalID,
					"decision":   event.ApprovalDecision,
					"reason":     reason,
				})
			case EventRedraw:
				changed = true
			case EventScaleChanged:
				if event.Scale > 0 {
					renderer = render.NewRenderer(event.Scale)
					regionsApplied = false
					changed = true
				}
			}
		}
		if changed {
			resetAnimation(previous)
			renderFrame()
		}
	}

	renderFrame()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot := <-snapshots:
			previous := currentAnimation()
			next := overlay.Present(snapshot)
			if next.SelectedPetPath != presentation.SelectedPetPath {
				sheet, err := render.LoadSpriteSheet(next.SelectedPetPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "pi-pet-overlay: load pet %q: %v\n", next.SelectedPetPath, err)
					sheet = nil
				}
				renderer.SetSheet(sheet)
				frameIndex = 0
			}
			// macOS parity: an auto-clearing bubble (done/failed) survives
			// the daemon settling back to idle until its timer fires.
			if next.StateID == "idle" && next.Bubble == "" && bubbleClearCh != nil {
				next.Bubble = presentation.Bubble
			} else if next.Bubble != "" && next.AutoClearSeconds > 0 {
				scheduleBubbleClear(next.AutoClearSeconds)
			} else if next.Bubble != presentation.Bubble {
				cancelBubbleClear()
			}
			presentation = next
			updateState = snapshot.Update
			if updateState != nil && updateState.Stage == "restartPending" && !relaunchScheduled {
				relaunchScheduled = true
				go relaunchSelf()
			}
			if !requestedFirstCheck {
				requestedFirstCheck = true
				go sendRequestOnce(socketPath, protocol.MethodUpdateCheck)
			}
			resetAnimation(previous)
			renderFrame()
		case result := <-interactionResults:
			previous := currentAnimation()
			if result.StateID != "" && result.DurationSeconds > 0 {
				localPose = result.StateID
				localPoseUntil = time.Now().Add(time.Duration(result.DurationSeconds * float64(time.Second)))
			}
			if result.Murmur != "" && presentation.Bubble == "" {
				presentation.Bubble = result.Murmur
				scheduleBubbleClear(6)
			}
			resetAnimation(previous)
			renderFrame()
		case <-bubbleClearCh:
			cancelBubbleClear()
			presentation.Bubble = ""
			renderFrame()
		case <-frameTimer.C:
			switch currentAnimation().playback {
			case playbackStatic:
				frameIndex = 0
			case playbackPlayOnce:
				state := render.StateByID(currentState())
				if playOnceDone {
					frameIndex = 0
				} else if frameIndex >= state.Frames-1 {
					frameIndex = 0
					playOnceDone = true
				} else {
					frameIndex++
				}
			default:
				frameIndex++
			}
			frameTimer.Reset(render.StateByID(currentState()).FrameDuration)
			renderFrame()
		case <-eventTicker.C:
			handleEvents()
		case <-updateTicker.C:
			go sendRequestOnce(socketPath, protocol.MethodUpdateCheck)
		}
	}
}

// dragStateForDelta picks the run direction while the pet is carried.
func dragStateForDelta(deltaX int, previous string) string {
	switch {
	case deltaX < 0:
		return "running-left"
	case deltaX > 0:
		return "running-right"
	case previous != "":
		return previous
	default:
		return "running-right"
	}
}

// relaunchSelf re-executes the freshly built binary after update.apply; the
// embedded daemon re-creates its socket on startup.
func relaunchSelf() {
	time.Sleep(1500 * time.Millisecond)
	executable, err := os.Executable()
	if err != nil {
		return
	}
	_ = syscall.Exec(executable, os.Args, os.Environ())
}
