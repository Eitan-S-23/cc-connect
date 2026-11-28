package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type processIdentity struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
}

type restartTask struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

type restartPlan struct {
	ID               string          `json:"id"`
	Config           string          `json:"config"`
	Binary           string          `json:"binary"`
	ControllerBinary string          `json:"controller_binary"`
	DataDir          string          `json:"data_dir"`
	Version          string          `json:"version"`
	SHA256           string          `json:"sha256"`
	Task             restartTask     `json:"task"`
	Main             processIdentity `json:"main"`
	Supervisor       processIdentity `json:"supervisor"`
	Created          time.Time       `json:"created"`
	Timeout          time.Duration   `json:"timeout"`
}

type restartReady struct {
	ID         string          `json:"id"`
	Version    string          `json:"version"`
	SHA256     string          `json:"sha256"`
	Main       processIdentity `json:"main"`
	Supervisor processIdentity `json:"supervisor"`
	Error      string          `json:"error,omitempty"`
}

type restartResult struct {
	ID       string `json:"id"`
	Error    string `json:"error,omitempty"`
	Rollback string `json:"rollback,omitempty"`
}

func restartDirectory(config string) string {
	return filepath.Join(filepath.Dir(config), "."+filepath.Base(config)+".restart")
}

func writeRestartJSON(path string, value any) error {
	if err := core.CheckUpdatePath(path); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restart-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readRestartJSON(path string, value any) error {
	if err := core.CheckUpdatePath(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return fmt.Errorf("invalid restart metadata: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

// PreparedRestart owns an independent task controller. Commit is called only
// after business state has been flushed and all services have been stopped.
type PreparedRestart struct {
	directory string
	id        string
}

func (p *PreparedRestart) Commit() error {
	if p == nil {
		return nil
	}
	return writeRestartJSON(filepath.Join(p.directory, "shutdown-ready.json"), restartResult{ID: p.id})
}
