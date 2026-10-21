package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/psumo/overclaud-installer/internal/deploy"
)

// Record is ~/.overclaud/state.json: what is installed and what the last deploy wrote.
type Record struct {
	Deploy     deploy.State `json:"deploy"`
	Commit     string       `json:"commit"`
	UpdatedAt  string       `json:"updated_at"`
	Conflicts  []string     `json:"conflicts,omitempty"`
	LastBackup string       `json:"last_backup,omitempty"`
}

func statePath(ocHome string) string { return filepath.Join(ocHome, "state.json") }

func loadState(ocHome string) Record {
	var r Record
	if data, err := os.ReadFile(statePath(ocHome)); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

func saveState(ocHome string, r Record) error {
	r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(ocHome, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	tmp := statePath(ocHome) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(ocHome))
}
