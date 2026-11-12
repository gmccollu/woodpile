package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// StateFileName is the run-state file a running server keeps in its data dir
// for `woodshed status`. It matters when the API can't answer, for example when
// the UI port is taken by another program: status then reads the UI error here.
const StateFileName = "woodshed.state.json"

// RunState is the content of the run-state file.
type RunState struct {
	PID       int        `json:"pid"`
	Version   string     `json:"version"`
	StartedAt time.Time  `json:"started_at"`
	Config    string     `json:"config"`
	DataDir   string     `json:"data_dir"`
	UI        RunStateUI `json:"ui"`
}

// RunStateUI is the UI server's state.
type RunStateUI struct {
	Listening bool   `json:"listening"`
	URL       string `json:"url"`
	LANMode   bool   `json:"lan_mode"`
	Error     string `json:"error,omitempty"`
	// CertFingerprint is the SHA-256 of the LAN-mode certificate (AB:CD:…).
	CertFingerprint string `json:"cert_fingerprint,omitempty"`
}

func writeState(dataDir string, rs RunState) error {
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, StateFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeState(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, StateFileName))
}

// ReadState reads the run-state file in dataDir. The file only means something
// while DataDirInUse reports true: a crashed server leaves it behind.
func ReadState(dataDir string) (RunState, error) {
	var rs RunState
	b, err := os.ReadFile(filepath.Join(dataDir, StateFileName))
	if err != nil {
		return rs, err
	}
	if err := json.Unmarshal(b, &rs); err != nil {
		return rs, errors.New("run-state file is malformed: " + err.Error())
	}
	return rs, nil
}
