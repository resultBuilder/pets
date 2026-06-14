package protocol

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	wantPayload := SessionUpsert{
		SessionID:   "s1",
		CWD:         "/repo",
		Title:       "tests",
		Status:      SessionRunning,
		SafeSummary: "running tests",
	}
	msg, err := NewRequest("1", MethodSessionUpsert, wantPayload)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	line, err := EncodeLine(msg)
	if err != nil {
		t.Fatalf("EncodeLine: %v", err)
	}

	got, err := DecodeLine(line)
	if err != nil {
		t.Fatalf("DecodeLine: %v", err)
	}
	if got.Version != Version || got.Kind != KindRequest || got.Method != MethodSessionUpsert || got.ID != "1" {
		t.Fatalf("unexpected message: %+v", got)
	}

	payload, err := DecodePayload[SessionUpsert](got)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload != wantPayload {
		t.Fatalf("payload mismatch: got %+v want %+v", payload, wantPayload)
	}
}

func TestDecodeLineRejectsUnsupportedVersion(t *testing.T) {
	data, err := json.Marshal(Message{Version: 99, Kind: KindRequest, ID: "1", Method: MethodHello})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLine(data); err == nil {
		t.Fatal("expected unsupported version error")
	}
}
