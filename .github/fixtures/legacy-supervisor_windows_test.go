//go:build windows

// Copied only into the baseline worktree by Windows Actions. RunSupervisor and
// its Job Object implementation come from the original e2b9795 source.
package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "_daemon-supervise" {
		exe, _ := os.Executable()
		config := os.Args[3]
		if RunSupervisor(SupervisorConfig{BinaryPath: exe, ConfigPath: config, WorkDir: filepath.Dir(config), LogFile: filepath.Join(filepath.Dir(config), "legacy.log"), RestartDelay: time.Second}) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "--config" {
		if WaitForSupervisorStartGate() != nil {
			os.Exit(2)
		}
		dir := filepath.Dir(os.Args[2])
		identity := func(pid uint32) map[string]any {
			h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if e != nil {
				os.Exit(3)
			}
			defer windows.CloseHandle(h)
			var created, exit, kernel, user windows.Filetime
			if windows.GetProcessTimes(h, &created, &exit, &kernel, &user) != nil {
				os.Exit(3)
			}
			return map[string]any{"pid": pid, "created": uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}
		}
		data, _ := json.Marshal(map[string]any{"version": "LEGACY", "main": identity(uint32(os.Getpid())), "supervisor": identity(uint32(os.Getppid()))})
		if os.WriteFile(filepath.Join(dir, "fixture-ready.json"), data, 0o600) != nil {
			os.Exit(4)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "legacy-exit")); err == nil {
				os.Exit(0)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}
