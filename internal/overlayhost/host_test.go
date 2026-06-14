package overlayhost

import "testing"

func TestDragStateForDelta(t *testing.T) {
	if got := dragStateForDelta(8, "running-left"); got != "running-right" {
		t.Fatalf("right drag state = %q", got)
	}
	if got := dragStateForDelta(-8, "running-right"); got != "running-left" {
		t.Fatalf("left drag state = %q", got)
	}
	if got := dragStateForDelta(0, "running-left"); got != "running-left" {
		t.Fatalf("zero-delta drag state should preserve previous, got %q", got)
	}
	if got := dragStateForDelta(0, ""); got != "running-right" {
		t.Fatalf("zero-delta default drag state = %q", got)
	}
}

func TestPlaybackForStateMatchesMacOSIdlePolicy(t *testing.T) {
	tests := []struct {
		name      string
		stateID   string
		transient bool
		want      playbackMode
	}{
		{name: "stable idle is static", stateID: "idle", want: playbackStatic},
		{name: "idle pulse plays once", stateID: "idle", transient: true, want: playbackPlayOnce},
		{name: "waiting plays once", stateID: "waiting", want: playbackPlayOnce},
		{name: "review plays once", stateID: "review", want: playbackPlayOnce},
		{name: "running loops", stateID: "running", want: playbackLoop},
		{name: "drag direction loops", stateID: "running-left", want: playbackLoop},
	}

	for _, test := range tests {
		if got := playbackForState(test.stateID, test.transient); got != test.want {
			t.Fatalf("%s: playback = %v, want %v", test.name, got, test.want)
		}
	}
}
