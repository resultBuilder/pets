package daemon

import (
	"time"

	"codex-pets/internal/petbrain"
	"codex-pets/internal/protocol"
)

const murmurAutoClearSeconds = 6

// AttachBrain gives the store a temperament: codex-state murmurs merge into
// the presentation bubble and interactions produce short presentation
// pulses. Persisted overlay settings (attention/bubble mode, reduce motion)
// are applied to the brain. asyncBroadcast publishes snapshots produced
// outside request handlers (pulse expiry, idle murmurs).
func (s *Store) AttachBrain(brain *petbrain.Brain, asyncBroadcast func(protocol.Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistedSettings.AttentionMode != "" {
		brain.Mode = petbrain.ParseAttentionMode(s.persistedSettings.AttentionMode)
	}
	if s.persistedSettings.BubbleMode != "" {
		brain.BubbleModeV = petbrain.ParseBubbleMode(s.persistedSettings.BubbleMode)
	}
	brain.ReduceMotion = s.persistedSettings.ReduceMotion
	s.brain = brain
	s.asyncBroadcast = asyncBroadcast
	s.finishMutationLocked(s.now().UTC())
}

// applyBrainLocked runs inside finishMutationLocked: it lets the brain react
// to the freshly computed presentation state and applies any active pulse.
func (s *Store) applyBrainLocked(ts time.Time) {
	if s.brain != nil {
		decision := s.brain.HandleCodexState(s.snapshot.Presentation.StateID)
		if decision != nil && decision.Bubble != "" && s.snapshot.Presentation.Bubble == "" {
			s.setPulseLocked(&presentationPulse{
				bubble: decision.Bubble,
				until:  ts.Add(murmurAutoClearSeconds * time.Second),
			})
		}
	}
	if s.pulse != nil && !ts.Before(s.pulse.until) {
		s.pulse = nil
	}
	// Murmurs never displace a status bubble and never touch the pose.
	if s.pulse != nil && s.snapshot.Presentation.Bubble == "" {
		s.snapshot.Presentation.Bubble = s.pulse.bubble
		s.snapshot.Presentation.AutoClearSeconds = s.pulse.until.Sub(ts).Seconds()
	}
}

func (s *Store) setPulseLocked(pulse *presentationPulse) {
	s.pulse = pulse
	if s.pulseTimer != nil {
		s.pulseTimer.Stop()
	}
	duration := pulse.until.Sub(s.now())
	if duration < 0 {
		duration = 0
	}
	s.pulseTimer = time.AfterFunc(duration+50*time.Millisecond, s.expirePulse)
}

// expirePulse re-derives the presentation once a pulse runs out so
// subscribers see the bubble clear from the snapshot too.
func (s *Store) expirePulse() {
	s.mu.Lock()
	if s.pulse == nil {
		s.mu.Unlock()
		return
	}
	s.finishMutationLocked(s.now().UTC())
	snapshot := cloneSnapshot(s.snapshot)
	broadcast := s.asyncBroadcast
	s.mu.Unlock()
	if broadcast != nil {
		broadcast(snapshot)
	}
}

// HandleInteraction picks the pet's reaction to overlay input. It returns
// the murmur (and a pose hint for brainless renderers) directly to the
// caller and deliberately does not touch the shared presentation: the
// interactive feedback loop is renderer-local, and a daemon-side pose would
// fight it (and mask real agent states like running).
func (s *Store) HandleInteraction(input protocol.OverlayInteraction) protocol.InteractionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.brain == nil {
		return protocol.InteractionResult{}
	}

	var decision *petbrain.Decision
	switch input.Type {
	case protocol.InteractionClick:
		decision = s.brain.HandleClick(input.Clicks)
	case protocol.InteractionDrag:
		decision = s.brain.HandleDrag()
	case protocol.InteractionMouseNear:
		decision = s.brain.HandleMouseNear()
	case protocol.InteractionUserReturned:
		decision = s.brain.HandleUserReturned()
	}
	if decision == nil {
		return protocol.InteractionResult{}
	}
	return protocol.InteractionResult{
		Murmur:          decision.Bubble,
		StateID:         decision.StateID,
		DurationSeconds: decision.Duration,
	}
}

// IdlePulse drives ambient behavior; the server calls it on a slow ticker.
func (s *Store) IdlePulse() (protocol.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.brain == nil {
		return cloneSnapshot(s.snapshot), false
	}
	decision := s.brain.HandleIdlePulse()
	if decision == nil || decision.Bubble == "" {
		return cloneSnapshot(s.snapshot), false
	}
	s.setPulseLocked(&presentationPulse{
		bubble: decision.Bubble,
		until:  s.now().Add(murmurAutoClearSeconds * time.Second),
	})
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot), true
}

// SetOverlaySettings updates and persists how lively and chatty the pet is.
func (s *Store) SetOverlaySettings(input protocol.OverlaySettings) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.AttentionMode != "" {
		s.persistedSettings.AttentionMode = string(petbrain.ParseAttentionMode(input.AttentionMode))
	}
	if input.BubbleMode != "" {
		s.persistedSettings.BubbleMode = string(petbrain.ParseBubbleMode(input.BubbleMode))
	}
	if input.ReduceMotion != nil {
		s.persistedSettings.ReduceMotion = *input.ReduceMotion
	}
	if s.brain != nil {
		if s.persistedSettings.AttentionMode != "" {
			s.brain.Mode = petbrain.ParseAttentionMode(s.persistedSettings.AttentionMode)
		}
		if s.persistedSettings.BubbleMode != "" {
			s.brain.BubbleModeV = petbrain.ParseBubbleMode(s.persistedSettings.BubbleMode)
		}
		s.brain.ReduceMotion = s.persistedSettings.ReduceMotion
	}
	s.savePersistedStateLocked()
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}

// MuteMurmurs silences murmurs until local midnight or for input.Seconds.
func (s *Store) MuteMurmurs(input protocol.MurmursMute) protocol.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.brain != nil {
		if input.Today || input.Seconds <= 0 {
			s.brain.MuteMurmursForToday()
		} else {
			s.brain.MuteMurmurs(input.Seconds)
		}
	}
	s.pulse = nil
	s.finishMutationLocked(s.now().UTC())
	return cloneSnapshot(s.snapshot)
}
