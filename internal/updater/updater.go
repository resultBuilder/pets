package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"codex-pets/internal/protocol"
)

// Stage values mirrored into protocol.UpdateState.Stage.
const (
	StageChecking       = "checking"
	StageFetching       = "fetching"
	StagePulling        = "pulling"
	StageBuilding       = "building"
	StageRestartPending = "restartPending"
	StageFailed         = "failed"
)

const markerRelativePath = "build/.codex-pets-built-commit"

// Updater implements the shared self-update pipeline: detect upstream
// commits (or a diverged built-commit marker), fast-forward the checkout,
// run the host-provided build script, and record the built commit. Hosts
// stay responsible only for triggering checks and restarting themselves
// when the state reaches StageRestartPending.
type Updater struct {
	repoRoot    string
	buildScript string

	mu      sync.Mutex
	busy    bool
	state   protocol.UpdateState
	onState func(protocol.UpdateState)
}

// New returns an updater rooted at repoRoot. buildScript is resolved
// relative to repoRoot when not absolute. onState observes every state
// transition (the daemon publishes them into the snapshot).
func New(repoRoot string, buildScript string, onState func(protocol.UpdateState)) *Updater {
	if !filepath.IsAbs(buildScript) && buildScript != "" {
		buildScript = filepath.Join(repoRoot, buildScript)
	}
	return &Updater{
		repoRoot:    repoRoot,
		buildScript: buildScript,
		onState:     onState,
	}
}

// DetectRepoRoot walks up from startPath looking for a .git directory, the
// same way every host binary lives inside the repo's build output.
func DetectRepoRoot(startPath string) string {
	path := startPath
	for i := 0; i < 10; i++ {
		path = filepath.Dir(path)
		if path == "/" || path == "." {
			break
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			return path
		}
	}
	return ""
}

func (u *Updater) State() protocol.UpdateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// Check fetches the upstream and reports whether an update (or a rebuild
// after a diverged marker) is available. Runs synchronously; failures fall
// back to the idle state, matching the previous host behavior.
func (u *Updater) Check() protocol.UpdateState {
	u.mu.Lock()
	if u.busy {
		state := u.state
		u.mu.Unlock()
		return state
	}
	u.busy = true
	u.mu.Unlock()
	defer u.setBusy(false)

	u.setState(protocol.UpdateState{Stage: StageChecking})

	// Best effort: an unreachable remote (offline, VPN down) must not hide
	// the purely local rebuild detection below; rev-list then evaluates
	// against the last fetched upstream state.
	_, _, _ = u.git("fetch", "--quiet")

	behindOut, _, _ := u.git("rev-list", "--count", "HEAD..@{u}")
	behind, _ := strconv.Atoi(strings.TrimSpace(behindOut))
	if behind > 0 {
		return u.setState(protocol.UpdateState{Available: true, CommitsBehind: behind})
	}

	// No upstream commits: offer a rebuild when the running binaries were
	// built from a commit that is no longer an ancestor of HEAD.
	if marker := u.readMarker(); marker != "" {
		if _, _, code := u.git("merge-base", "--is-ancestor", marker, "HEAD"); code != 0 {
			return u.setState(protocol.UpdateState{Available: true, CommitsBehind: 0})
		}
	}
	return u.setState(protocol.UpdateState{})
}

// Apply runs the update asynchronously; progress is reported through
// onState and the final state is StageRestartPending or StageFailed.
func (u *Updater) Apply() protocol.UpdateState {
	u.mu.Lock()
	if u.busy || !u.state.Available {
		state := u.state
		u.mu.Unlock()
		return state
	}
	u.busy = true
	behind := u.state.CommitsBehind
	u.mu.Unlock()

	go func() {
		defer u.setBusy(false)
		u.runApply(behind)
	}()
	return u.State()
}

func (u *Updater) runApply(behind int) {
	// Pull only when actually behind: the rebuild-only path must work on
	// branches without an upstream.
	if behind > 0 {
		u.setState(protocol.UpdateState{Stage: StageFetching})
		if _, stderr, code := u.git("fetch", "--quiet"); code != 0 {
			u.fail("git fetch failed: " + clampMessage(stderr))
			return
		}
		u.setState(protocol.UpdateState{Stage: StagePulling})
		if _, stderr, code := u.git("pull", "--ff-only", "--quiet"); code != 0 {
			u.fail("git pull failed: " + clampMessage(stderr))
			return
		}
	}

	u.setState(protocol.UpdateState{Stage: StageBuilding})
	if u.buildScript == "" {
		u.fail("no build script configured")
		return
	}
	if _, stderr, code := u.run("/bin/sh", u.buildScript); code != 0 {
		u.fail("build failed: " + clampMessage(stderr))
		return
	}

	u.writeMarker()
	u.setState(protocol.UpdateState{Stage: StageRestartPending})
}

// Dismiss clears a failed state so the host can retry.
func (u *Updater) Dismiss() protocol.UpdateState {
	u.mu.Lock()
	if u.state.Stage == StageFailed {
		u.state = protocol.UpdateState{}
	}
	state := u.state
	onState := u.onState
	u.mu.Unlock()
	if onState != nil {
		onState(state)
	}
	return state
}

func (u *Updater) fail(message string) {
	u.setState(protocol.UpdateState{Stage: StageFailed, Message: message})
}

func (u *Updater) setState(state protocol.UpdateState) protocol.UpdateState {
	u.mu.Lock()
	u.state = state
	onState := u.onState
	u.mu.Unlock()
	if onState != nil {
		onState(state)
	}
	return state
}

func (u *Updater) setBusy(busy bool) {
	u.mu.Lock()
	u.busy = busy
	u.mu.Unlock()
}

func (u *Updater) readMarker() string {
	data, err := os.ReadFile(filepath.Join(u.repoRoot, markerRelativePath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (u *Updater) writeMarker() {
	head, _, code := u.git("rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	if code != 0 || head == "" {
		return
	}
	path := filepath.Join(u.repoRoot, markerRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(head+"\n"), 0o644)
}

func (u *Updater) git(args ...string) (string, string, int) {
	return u.run("git", args...)
}

func (u *Updater) run(command string, args ...string) (string, string, int) {
	cmd := exec.Command(command, args...)
	cmd.Dir = u.repoRoot
	cmd.Env = augmentedEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}
	return stdout.String(), stderr.String(), 0
}

// augmentedEnv prepends the usual toolchain locations so git, swiftc, and
// go resolve even when the host app was launched with a minimal PATH.
func augmentedEnv() []string {
	extra := []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/bin", "/bin"}
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/bin:/bin"
	}
	merged := strings.Join(append(extra, current), ":")

	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "PATH="+merged)
}

func clampMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 120 {
		return value
	}
	return value[:120]
}
