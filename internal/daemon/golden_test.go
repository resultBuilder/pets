package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-pets/internal/protocol"
)

// Golden protocol fixtures are the cross-implementation conformance suite for
// the daemon: each fixture replays a request sequence over a real unix socket
// and compares the final snapshot (with volatile timestamp fields stripped).
// Any host that exposes the pi-pet protocol must produce these exact results.
//
// Fixture format (testdata/golden/*.json):
//
//	{
//	  "steps": [
//	    {"method": "session.upsert", "payload": {...}},
//	    {"method": "approval.request", "payload": {...}, "background": true,
//	     "expectDecision": {"decision": "approved", ...}},
//	    {"waitPendingApprovals": 1}
//	  ],
//	  "snapshot": { ...expected final snapshot, timestamps omitted... }
//	}
//
// Steps run sequentially on one connection. "background" sends the request on
// its own connection without waiting (for blocking approval.request calls);
// its response payload must contain every key/value in "expectDecision".
// "waitPendingApprovals" polls snapshot.get until the pending approval count
// matches.

type goldenFixture struct {
	Steps    []goldenStep   `json:"steps"`
	Snapshot map[string]any `json:"snapshot"`
}

type goldenStep struct {
	Method               string          `json:"method,omitempty"`
	Payload              json.RawMessage `json:"payload,omitempty"`
	Background           bool            `json:"background,omitempty"`
	ExpectDecision       map[string]any  `json:"expectDecision,omitempty"`
	WaitPendingApprovals *int            `json:"waitPendingApprovals,omitempty"`
}

type goldenBackgroundResult struct {
	message protocol.Message
	err     error
}

type goldenBackgroundCall struct {
	step    int
	expect  map[string]any
	results chan goldenBackgroundResult
}

func TestGoldenProtocolFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if err != nil {
		t.Fatalf("glob golden fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden fixtures found in testdata/golden")
	}
	for _, path := range paths {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			runGoldenFixture(t, path)
		})
	}
}

func runGoldenFixture(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture goldenFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Steps) == 0 || fixture.Snapshot == nil {
		t.Fatal("fixture needs steps and an expected snapshot")
	}

	socketPath, _ := startTestServer(t)
	client := newTestClient(t, socketPath)
	defer client.close()

	var backgrounds []goldenBackgroundCall
	for i, step := range fixture.Steps {
		switch {
		case step.WaitPendingApprovals != nil:
			waitGoldenPendingApprovals(t, client, i, *step.WaitPendingApprovals)
		case step.Background:
			results := make(chan goldenBackgroundResult, 1)
			backgrounds = append(backgrounds, goldenBackgroundCall{step: i, expect: step.ExpectDecision, results: results})
			go func(id string, method string, payload json.RawMessage) {
				message, err := goldenRawRequest(socketPath, id, method, payload)
				results <- goldenBackgroundResult{message: message, err: err}
			}(fmt.Sprintf("golden-bg-%d", i), step.Method, step.Payload)
		default:
			client.request(t, fmt.Sprintf("golden-%d", i), step.Method, step.Payload)
		}
	}

	for _, background := range backgrounds {
		select {
		case result := <-background.results:
			if result.err != nil {
				t.Fatalf("step %d background request: %v", background.step, result.err)
			}
			if result.message.Error != nil {
				t.Fatalf("step %d background request error: %s", background.step, result.message.Error.Message)
			}
			var payload map[string]any
			if err := json.Unmarshal(result.message.Payload, &payload); err != nil {
				t.Fatalf("step %d decode background payload: %v", background.step, err)
			}
			for key, want := range background.expect {
				if got, ok := payload[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("step %d decision %s = %v, want %v (payload %v)", background.step, key, got, want, payload)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("step %d background request did not finish", background.step)
		}
	}

	final := client.request(t, "golden-final", protocol.MethodSnapshotGet, nil)
	var got any
	if err := json.Unmarshal(final.Payload, &got); err != nil {
		t.Fatalf("decode final snapshot: %v", err)
	}
	got = stripGoldenTimestamps(got)
	want := stripGoldenTimestamps(anyValue(t, fixture.Snapshot))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final snapshot mismatch\n got: %s\nwant: %s", goldenJSON(t, got), goldenJSON(t, want))
	}
}

func waitGoldenPendingApprovals(t *testing.T, client *testClient, step int, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		response := client.request(t, fmt.Sprintf("golden-wait-%d-%d", step, time.Now().UnixNano()), protocol.MethodSnapshotGet, nil)
		var snapshot protocol.Snapshot
		if err := json.Unmarshal(response.Payload, &snapshot); err != nil {
			t.Fatalf("step %d decode snapshot: %v", step, err)
		}
		if len(snapshot.PendingApprovals) == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("step %d: pending approvals = %d, want %d", step, len(snapshot.PendingApprovals), count)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func goldenRawRequest(socketPath string, id string, method string, payload json.RawMessage) (protocol.Message, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return protocol.Message{}, err
	}
	defer conn.Close()
	msg, err := protocol.NewRequest(id, method, payload)
	if err != nil {
		return protocol.Message{}, err
	}
	line, err := protocol.EncodeLine(msg)
	if err != nil {
		return protocol.Message{}, err
	}
	if _, err := conn.Write(line); err != nil {
		return protocol.Message{}, err
	}
	reader := bufio.NewReader(conn)
	response, err := reader.ReadBytes('\n')
	if err != nil {
		return protocol.Message{}, err
	}
	return protocol.DecodeLine(response)
}

var goldenTimestampKeys = map[string]struct{}{
	"startedAt": {},
	"updatedAt": {},
	"createdAt": {},
	"endedAt":   {},
}

func stripGoldenTimestamps(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			if _, volatile := goldenTimestampKeys[key]; volatile {
				continue
			}
			out[key] = stripGoldenTimestamps(nested)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, nested := range typed {
			out[i] = stripGoldenTimestamps(nested)
		}
		return out
	default:
		return value
	}
}

func anyValue(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal expected snapshot: %v", err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal expected snapshot: %v", err)
	}
	return out
}

func goldenJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot diff: %v", err)
	}
	return string(data)
}
