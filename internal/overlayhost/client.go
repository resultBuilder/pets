package overlayhost

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"time"

	"codex-pets/internal/protocol"
)

// subscribeSnapshots keeps a state.subscribe connection to the daemon alive
// and forwards decoded snapshots, reconnecting with a fixed backoff.
func subscribeSnapshots(ctx context.Context, socketPath string, out chan<- protocol.Snapshot) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			sleepOrDone(ctx, 2*time.Second)
			continue
		}
		if err := writeRequest(conn, "state.subscribe", map[string]any{}); err != nil {
			conn.Close()
			sleepOrDone(ctx, 2*time.Second)
			continue
		}
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			snapshot, ok := decodeSnapshot(scanner.Bytes())
			if !ok {
				continue
			}
			select {
			case out <- snapshot:
			case <-ctx.Done():
				conn.Close()
				return
			}
		}
		conn.Close()
		sleepOrDone(ctx, 2*time.Second)
	}
}

// sendRequestOnce performs a single fire-and-forget request (update.check,
// update.apply, update.dismiss) on a short-lived connection; resulting state
// arrives through the snapshot subscription.
func sendRequestOnce(socketPath string, method string) {
	sendPayloadOnce(socketPath, method, map[string]any{})
}

// requestInteraction asks the daemon's brain how the pet reacts to input.
// The murmur and pose hint come back directly; the caller renders them
// locally so the shared presentation never flickers.
func requestInteraction(socketPath string, interaction protocol.OverlayInteraction) protocol.InteractionResult {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return protocol.InteractionResult{}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := map[string]any{"type": interaction.Type}
	if interaction.Clicks > 0 {
		payload["clicks"] = interaction.Clicks
	}
	if err := writeRequest(conn, protocol.MethodOverlayInteraction, payload); err != nil {
		return protocol.InteractionResult{}
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return protocol.InteractionResult{}
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
		return protocol.InteractionResult{}
	}
	var result protocol.InteractionResult
	_ = json.Unmarshal(envelope.Payload, &result)
	return result
}

func sendPayloadOnce(socketPath string, method string, payload map[string]any) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeRequest(conn, method, payload); err != nil {
		return
	}
	// Wait for the ack so the daemon has processed the request.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Scan()
}

func writeRequest(conn net.Conn, method string, payload map[string]any) error {
	message := map[string]any{
		"version": 1,
		"kind":    "request",
		"id":      "x11-1",
		"method":  method,
		"payload": payload,
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}

func decodeSnapshot(line []byte) (protocol.Snapshot, bool) {
	var envelope struct {
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return protocol.Snapshot{}, false
	}
	if envelope.Method != "state.snapshot" && envelope.Method != "state.subscribe" {
		return protocol.Snapshot{}, false
	}
	var snapshot protocol.Snapshot
	if err := json.Unmarshal(envelope.Payload, &snapshot); err != nil {
		return protocol.Snapshot{}, false
	}
	return snapshot, true
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
