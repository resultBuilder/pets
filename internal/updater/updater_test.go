package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-pets/internal/protocol"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{
		"-c", "user.email=test@codexpets",
		"-c", "user.name=CodexPets Test",
		"-c", "commit.gpgsign=false",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

type stateRecorder struct {
	mu     sync.Mutex
	states []protocol.UpdateState
}

func (r *stateRecorder) record(state protocol.UpdateState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
}

func (r *stateRecorder) contains(predicate func(protocol.UpdateState) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range r.states {
		if predicate(state) {
			return true
		}
	}
	return false
}

func setupRepos(t *testing.T) (originDir string, workDir string) {
	t.Helper()
	root := t.TempDir()
	originDir = filepath.Join(root, "origin")
	workDir = filepath.Join(root, "work")
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, originDir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(originDir, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, originDir, "add", ".")
	runGit(t, originDir, "commit", "--quiet", "-m", "v1")
	runGit(t, root, "clone", "--quiet", originDir, workDir)
	return originDir, workDir
}

func writeStubBuildScript(t *testing.T, workDir string) string {
	t.Helper()
	script := filepath.Join(workDir, "build-stub.sh")
	content := "#!/bin/sh\necho built > \"$(dirname \"$0\")/build/build-proof\"\n"
	if err := os.MkdirAll(filepath.Join(workDir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func waitForState(t *testing.T, recorder *stateRecorder, label string, predicate func(protocol.UpdateState) bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.contains(predicate) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; states=%+v", label, recorder.states)
}

func TestCheckAndApplyFollowsUpstream(t *testing.T) {
	originDir, workDir := setupRepos(t)
	buildScript := writeStubBuildScript(t, workDir)
	recorder := &stateRecorder{}
	u := New(workDir, buildScript, recorder.record)

	if state := u.Check(); state.Available {
		t.Fatalf("up-to-date clone should not offer an update, got %+v", state)
	}

	if err := os.WriteFile(filepath.Join(originDir, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, originDir, "commit", "--quiet", "-am", "v2")

	state := u.Check()
	if !state.Available || state.CommitsBehind != 1 {
		t.Fatalf("check after upstream commit = %+v, want available 1 behind", state)
	}

	u.Apply()
	waitForState(t, recorder, "restart pending", func(state protocol.UpdateState) bool {
		return state.Stage == StageRestartPending
	})
	if !recorder.contains(func(state protocol.UpdateState) bool { return state.Stage == StagePulling }) {
		t.Fatal("apply should pull from upstream")
	}
	if !recorder.contains(func(state protocol.UpdateState) bool { return state.Stage == StageBuilding }) {
		t.Fatal("apply should run the build script")
	}

	workHead := runGit(t, workDir, "rev-parse", "HEAD")
	originHead := runGit(t, originDir, "rev-parse", "HEAD")
	if workHead != originHead {
		t.Fatalf("apply should fast-forward to upstream HEAD: work=%s origin=%s", workHead, originHead)
	}
	if _, err := os.Stat(filepath.Join(workDir, "build", "build-proof")); err != nil {
		t.Fatalf("build script did not run: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(workDir, "build", ".codex-pets-built-commit"))
	if err != nil || strings.TrimSpace(string(marker)) != workHead {
		t.Fatalf("marker = %q (%v), want %s", marker, err, workHead)
	}
}

func TestCheckOffersRebuildForDivergedMarker(t *testing.T) {
	originDir, workDir := setupRepos(t)
	buildScript := writeStubBuildScript(t, workDir)
	recorder := &stateRecorder{}
	u := New(workDir, buildScript, recorder.record)

	// A marker pointing at a commit that is not an ancestor of HEAD (e.g.,
	// after a branch switch) must offer a rebuild without upstream commits.
	danglingHead := runGit(t, originDir, "commit-tree", runGit(t, originDir, "rev-parse", "HEAD^{tree}"), "-m", "dangling")
	markerPath := filepath.Join(workDir, "build", ".codex-pets-built-commit")
	if err := os.WriteFile(markerPath, []byte(danglingHead+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the dangling commit known to the clone so merge-base can judge it.
	runGit(t, workDir, "fetch", "--quiet", "origin", danglingHead)
	// An unreachable remote must not hide local rebuild detection.
	runGit(t, workDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))

	state := u.Check()
	if !state.Available || state.CommitsBehind != 0 {
		t.Fatalf("diverged marker check = %+v, want rebuild offer", state)
	}

	// Rebuild-only apply must not pull (works on branches without upstream).
	runGit(t, workDir, "branch", "--unset-upstream")
	u.Apply()
	waitForState(t, recorder, "rebuild restart pending", func(state protocol.UpdateState) bool {
		return state.Stage == StageRestartPending
	})
	if recorder.contains(func(state protocol.UpdateState) bool { return state.Stage == StagePulling }) {
		t.Fatal("rebuild-only apply should not pull")
	}
	if _, err := os.Stat(filepath.Join(workDir, "build", "build-proof")); err != nil {
		t.Fatalf("rebuild did not run the build script: %v", err)
	}
}

func TestApplyReportsBuildFailure(t *testing.T) {
	originDir, workDir := setupRepos(t)
	recorder := &stateRecorder{}
	failingScript := filepath.Join(workDir, "build-fail.sh")
	if err := os.WriteFile(failingScript, []byte("#!/bin/sh\necho broken >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := New(workDir, failingScript, recorder.record)

	if err := os.WriteFile(filepath.Join(originDir, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, originDir, "commit", "--quiet", "-am", "v2")

	if state := u.Check(); !state.Available {
		t.Fatalf("check = %+v, want available", state)
	}
	u.Apply()
	waitForState(t, recorder, "build failure", func(state protocol.UpdateState) bool {
		return state.Stage == StageFailed && strings.Contains(state.Message, "broken")
	})

	if state := u.Dismiss(); state.Stage != "" || state.Available {
		t.Fatalf("dismiss should reset failed state, got %+v", state)
	}
}

func TestDetectRepoRootWalksUp(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	nested := filepath.Join(repo, "build", "App.app", "Contents", "MacOS")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectRepoRoot(filepath.Join(nested, "binary")); got != repo {
		t.Fatalf("DetectRepoRoot = %q, want %q", got, repo)
	}
	if got := DetectRepoRoot(filepath.Join(root, "elsewhere", "binary")); got != "" {
		t.Fatalf("DetectRepoRoot outside a repo = %q, want empty", got)
	}
}
