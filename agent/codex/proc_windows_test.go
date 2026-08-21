//go:build windows

package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConfiguredCancelKillsWindowsProcessTree(t *testing.T) {
	switch os.Getenv("CC_CONNECT_PROCESS_TREE_HELPER") {
	case "child":
		select {}
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestConfiguredCancelKillsWindowsProcessTree")
		child.Env = append(os.Environ(), "CC_CONNECT_PROCESS_TREE_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("CC_CONNECT_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(3)
		}
		select {}
	}

	pidFile := t.TempDir() + "\\child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestConfiguredCancelKillsWindowsProcessTree")
	cmd.Env = append(os.Environ(),
		"CC_CONNECT_PROCESS_TREE_HELPER=parent",
		"CC_CONNECT_CHILD_PID_FILE="+pidFile,
	)
	prepareCmdForKill(cmd)
	configureCmdCancel(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper parent: %v", err)
	}

	childPID := waitForChildPID(t, pidFile)
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(childPID)).Run()
	})

	cancel()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("parent process did not exit after cancellation")
	}

	deadline := time.Now().Add(3 * time.Second)
	for windowsProcessExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessExists(childPID) {
		t.Fatalf("child process %d survived parent context cancellation", childPID)
	}
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				t.Fatalf("parse child PID: %v", convErr)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for child PID")
	return 0
}

func windowsProcessExists(pid int) bool {
	probe := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pid))
	return probe.Run() == nil
}
