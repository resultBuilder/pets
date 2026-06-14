package linuxpetdex

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

type smokeRecorder struct {
	path string
	mu   sync.Mutex
}

func newSmokeRecorder(path string) *smokeRecorder {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_ = os.WriteFile(path, nil, 0o600)
	return &smokeRecorder{path: path}
}

func (r *smokeRecorder) close() {}

func (b *browser) scheduleSmokeReport() {
	if b.smoke == nil {
		return
	}
	b.evaluate(`
setTimeout(() => {
  window.CodexPetsNative?.postMessage({
    action: "smokeReport",
    event: "petdexBrowser",
    bootStarted: Boolean(window.__codexPetsAppBoot?.started),
    bootLoaded: Boolean(window.__codexPetsAppBoot?.loaded),
    bootError: window.__codexPetsAppBoot?.error || "",
    rowCount: document.querySelectorAll(".pet-row").length,
    status: document.querySelector("#status")?.textContent?.trim() || "",
    selectedName: document.querySelector("#pet-name")?.textContent?.trim() || ""
  });
}, 250);
`)
}

func (b *browser) recordSmokeReport(message bridgeMessage) {
	if b.smoke == nil {
		return
	}
	b.smoke.write(map[string]any{
		"event":        firstNonEmpty(message.Event, "petdexBrowser"),
		"bootStarted":  message.BootStarted,
		"bootLoaded":   message.BootLoaded,
		"bootError":    message.BootError,
		"rowCount":     message.RowCount,
		"status":       message.Status,
		"selectedName": message.SelectedName,
	})
}

func (r *smokeRecorder) write(fields map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fields["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(fields)
	if err != nil {
		return
	}
	data = append(data, '\n')
	file, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(data)
}
