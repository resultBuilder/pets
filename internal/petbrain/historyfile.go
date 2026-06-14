package petbrain

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LoadHistoryFile reads a murmur history JSON (the same format the macOS
// app persisted), returning a fresh history when the file is absent or
// unreadable — murmurs are delight, not critical state.
func LoadHistoryFile(path string) *History {
	if path == "" {
		return NewHistory()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return NewHistory()
	}
	history := NewHistory()
	if err := json.Unmarshal(data, history); err != nil {
		return NewHistory()
	}
	history.ensureMaps()
	return history
}

// SaveHistoryFile persists the murmur history atomically; failures are
// silently ignored for the same reason loads are forgiving.
func SaveHistoryFile(path string, history *History) {
	if path == "" || history == nil {
		return
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, append(data, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tempPath, path)
}

// NewWithHistoryFile builds a production brain that loads and persists its
// murmur history at path.
func NewWithHistoryFile(path string) *Brain {
	return New(Options{
		History: LoadHistoryFile(path),
		OnHistory: func(history *History) {
			SaveHistoryFile(path, history)
		},
	})
}
