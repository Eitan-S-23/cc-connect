//go:build windows

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	supervisorHelperEnv     = "CC_CONNECT_SUPERVISOR_TEST_HELPER"
	supervisorHelperPIDFile = "CC_CONNECT_SUPERVISOR_TEST_PID_FILE"
	supervisorHelperDelay   = "CC_CONNECT_SUPERVISOR_TEST_DELAY"
)

func TestRequireSupervisedElevationRejectsLimitedToken(t *testing.T) {
	orig := currentProcessElevated
	t.Cleanup(func() { currentProcessElevated = orig })
	t.Setenv(supervisedProcessEnv, "1")
	currentProcessElevated = func() bool { return false }

	if err := RequireSupervisedElevation(); err == nil || !strings.Contains(err.Error(), "elevated") {
		t.Fatalf("RequireSupervisedElevation() error = %v, want elevated-token error", err)
	}
	currentProcessElevated = func() bool { return true }
	if err := RequireSupervisedElevation(); err != nil {
		t.Fatalf("RequireSupervisedElevation() with elevated token: %v", err)
	}
}

func TestAcquireSupervisorLockRejectsDuplicateConfig(t *testing.T) {
	configPath := t.TempDir() + "\\config.toml"
	first, err := acquireSupervisorLock(configPath)
	if err != nil {
		t.Fatalf("acquire first supervisor lock: %v", err)
	}
	defer first.Release()

	if second, err := acquireSupervisorLock(strings.ToUpper(configPath)); err == nil {
		second.Release()
		t.Fatal("second supervisor lock for the same case-insensitive path succeeded")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second supervisor lock error = %v, want already-running error", err)
	}

	first.Release()
	third, err := acquireSupervisorLock(configPath)
	if err != nil {
		t.Fatalf("reacquire supervisor lock after release: %v", err)
	}
	third.Release()
}

func TestWaitForSupervisorStartGate(t *testing.T) {
	name := "Local\\cc-connect-supervisor-test-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := windows.CreateEvent(nil, 1, 0, namePtr)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(gate)
	t.Setenv(supervisorStartGateEnv, name)

	if err := windows.SetEvent(gate); err != nil {
		t.Fatal(err)
	}
	if err := WaitForSupervisorStartGate(); err != nil {
		t.Fatalf("WaitForSupervisorStartGate(): %v", err)
	}
	if got := os.Getenv(supervisorStartGateEnv); got != "" {
		t.Fatalf("start gate env survived release: %q", got)
	}
}

func TestRunCommandInKillJobCleansDescendants(t *testing.T) {
	switch os.Getenv(supervisorHelperEnv) {
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	case "gated-parent":
		if err := WaitForSupervisorStartGate(); err != nil {
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=TestRunCommandInKillJobCleansDescendants")
		child.Env = setWindowsEnv(os.Environ(), map[string]string{supervisorHelperEnv: "child"})
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv(supervisorHelperPIDFile), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(4)
		}
		return
	}

	pidFile := t.TempDir() + "\\child.pid"
	cmd := exec.Command(os.Args[0], "-test.run=TestRunCommandInKillJobCleansDescendants")
	cmd.Env = setWindowsEnv(os.Environ(), map[string]string{
		supervisorHelperEnv:     "gated-parent",
		supervisorHelperPIDFile: pidFile,
	})
	if err := runCommandInKillJob(cmd); err != nil {
		t.Fatalf("runCommandInKillJob(): %v", err)
	}
	childPID := waitForSupervisorHelperPID(t, pidFile)
	if windowsProcessAlive(uint32(childPID)) {
		t.Fatalf("descendant pid %d survived supervised root exit", childPID)
	}
}

func TestAssignExistingProcessTreeCleansPreexistingDescendant(t *testing.T) {
	switch os.Getenv(supervisorHelperEnv) {
	case "existing-child":
		for {
			time.Sleep(time.Hour)
		}
	case "existing-parent":
		child := exec.Command(os.Args[0], "-test.run=TestAssignExistingProcessTreeCleansPreexistingDescendant")
		child.Env = setWindowsEnv(os.Environ(), map[string]string{supervisorHelperEnv: "existing-child"})
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv(supervisorHelperPIDFile), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(4)
		}
		for {
			time.Sleep(time.Hour)
		}
	}

	pidFile := t.TempDir() + "\\child.pid"
	root := exec.Command(os.Args[0], "-test.run=TestAssignExistingProcessTreeCleansPreexistingDescendant")
	root.Env = setWindowsEnv(os.Environ(), map[string]string{
		supervisorHelperEnv:     "existing-parent",
		supervisorHelperPIDFile: pidFile,
	})
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := waitForSupervisorHelperPID(t, pidFile)
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(root.Process.Pid)).Run()
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(childPID)).Run()
	})

	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if err := assignExistingProcessTree(job, uint32(root.Process.Pid)); err != nil {
		t.Fatalf("assignExistingProcessTree(): %v", err)
	}
	if err := root.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = root.Process.Wait()
	if err := terminateAndDrainJob(job); err != nil {
		t.Fatalf("terminateAndDrainJob(): %v", err)
	}
	if windowsProcessAlive(uint32(childPID)) {
		t.Fatalf("preexisting descendant pid %d survived handoff cleanup", childPID)
	}
}

func TestAdoptExistingProcessTreeFallsBackForNestedJob(t *testing.T) {
	switch os.Getenv(supervisorHelperEnv) {
	case "fallback-child":
		for {
			time.Sleep(time.Hour)
		}
	case "fallback-parent":
		child := exec.Command(os.Args[0], "-test.run=TestAdoptExistingProcessTreeFallsBackForNestedJob")
		child.Env = setWindowsEnv(os.Environ(), map[string]string{supervisorHelperEnv: "fallback-child"})
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv(supervisorHelperPIDFile), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(4)
		}
		delay, _ := strconv.Atoi(os.Getenv(supervisorHelperDelay))
		time.Sleep(time.Duration(delay) * time.Millisecond)
		return
	}

	pidFile := t.TempDir() + "\\child.pid"
	root := exec.Command(os.Args[0], "-test.run=TestAdoptExistingProcessTreeFallsBackForNestedJob")
	root.Env = setWindowsEnv(os.Environ(), map[string]string{
		supervisorHelperEnv:     "fallback-parent",
		supervisorHelperPIDFile: pidFile,
		supervisorHelperDelay:   "500",
	})
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := waitForSupervisorHelperPID(t, pidFile)
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(root.Process.Pid)).Run()
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(childPID)).Run()
	})

	origAssign := assignProcessToJobObject
	assignProcessToJobObject = func(windows.Handle, windows.Handle) error {
		return windows.ERROR_ACCESS_DENIED
	}
	t.Cleanup(func() { assignProcessToJobObject = origAssign })

	started := time.Now()
	if err := adoptAndWaitForProcessTree(uint32(root.Process.Pid)); err != nil {
		t.Fatalf("adoptAndWaitForProcessTree() fallback: %v", err)
	}
	if err := root.Wait(); err != nil {
		t.Fatalf("wait for fallback root: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
		t.Fatalf("fallback returned after %s; root process was likely terminated before it exited", elapsed)
	}
	if windowsProcessAlive(uint32(childPID)) {
		t.Fatalf("fallback watcher left descendant pid %d alive", childPID)
	}
}

func TestDescendantPIDsHandlesCycles(t *testing.T) {
	got := descendantPIDs(1, map[uint32][]uint32{
		1: {2, 3},
		2: {4},
		3: {1},
		4: {2},
	})
	want := []uint32{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("descendantPIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descendantPIDs() = %v, want %v", got, want)
		}
	}
}

func waitForSupervisorHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for helper PID file %s", path)
	return 0
}

func windowsProcessAlive(pid uint32) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && !errors.Is(err, windows.ERROR_NOT_FOUND)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == 259 // STILL_ACTIVE
}
