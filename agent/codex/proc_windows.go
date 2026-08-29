//go:build windows

package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func configureCmdCancel(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Cancel = func() error {
		return forceKillCmd(cmd)
	}
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	rootPID := uint32(cmd.Process.Pid)
	children, treeErr := windowsProcessChildren()
	killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	fallbackErr := treeErr
	if fallbackErr == nil {
		fallbackErr = killWindowsProcessTreeFromSnapshot(rootPID, children)
	}
	if fallbackErr == nil {
		return nil
	}
	if killErr := cmd.Process.Kill(); killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("taskkill failed and process-tree fallback was incomplete: %w: %s; fallback: %v", err, processKillOutput(output), fallbackErr)
	} else {
		return fmt.Errorf("taskkill failed: %w: %s; process-tree fallback: %v; process kill fallback failed: %w", err, processKillOutput(output), fallbackErr, killErr)
	}
}

func killWindowsProcessTreeFromSnapshot(rootPID uint32, children map[uint32][]uint32) error {
	visited := make(map[uint32]bool)
	var errs []error
	var kill func(uint32)
	kill = func(pid uint32) {
		if visited[pid] {
			return
		}
		visited[pid] = true
		for _, childPID := range children[pid] {
			kill(childPID)
		}
		if err := terminateWindowsProcess(pid); err != nil {
			errs = append(errs, fmt.Errorf("pid %d: %w", pid, err))
		}
	}
	kill(rootPID)
	return errors.Join(errs...)
}

func windowsProcessChildren() (map[uint32][]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	children := make(map[uint32][]uint32)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		entry.Size = uint32(unsafe.Sizeof(windows.ProcessEntry32{}))
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("enumerate processes: %w", err)
		}
	}
	return children, nil
}

func terminateWindowsProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	status, err := windows.WaitForSingleObject(handle, 2000)
	if err != nil {
		return err
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("process did not exit after termination")
	}
	return nil
}

func processKillOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(empty output)"
	}
	return trimmed
}
