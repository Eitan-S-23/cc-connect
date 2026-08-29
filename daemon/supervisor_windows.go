//go:build windows

package daemon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	supervisorGateTimeout     = 30 * time.Second
	supervisorLockPoll        = 250 * time.Millisecond
	supervisorJobDrainTimeout = 5 * time.Second
	supervisorFallbackPoll    = 100 * time.Millisecond
	supervisorFallbackDrain   = 5 * time.Second
)

var currentProcessElevated = func() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

var assignProcessToJobObject = windows.AssignProcessToJobObject

type supervisorInstanceLock struct {
	handle windows.Handle
}

func acquireSupervisorLock(configPath string) (*supervisorInstanceLock, error) {
	key := strings.ToLower(filepath.Clean(configPath))
	hash := sha256.Sum256([]byte(key))
	name := fmt.Sprintf("Global\\cc-connect-supervisor-%x", hash[:16])
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("create supervisor lock name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, true, namePtr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return nil, fmt.Errorf("another cc-connect supervisor is already running for config %s", configPath)
	}
	if err != nil {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return nil, fmt.Errorf("create supervisor lock: %w", err)
	}
	return &supervisorInstanceLock{handle: handle}, nil
}

func (l *supervisorInstanceLock) Release() {
	if l == nil || l.handle == 0 {
		return
	}
	_ = windows.ReleaseMutex(l.handle)
	windows.CloseHandle(l.handle)
	l.handle = 0
}

func RequireSupervisedElevation() error {
	if os.Getenv(supervisedProcessEnv) == "" {
		return nil
	}
	if !currentProcessElevated() {
		return fmt.Errorf("daemon-supervised cc-connect requires an elevated administrator token")
	}
	return nil
}

func WaitForSupervisorStartGate() error {
	gateName := os.Getenv(supervisorStartGateEnv)
	if gateName == "" {
		return nil
	}
	defer os.Unsetenv(supervisorStartGateEnv)

	name, err := windows.UTF16PtrFromString(gateName)
	if err != nil {
		return fmt.Errorf("open daemon start gate: %w", err)
	}
	gate, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return fmt.Errorf("open daemon start gate: %w", err)
	}
	defer windows.CloseHandle(gate)

	status, err := windows.WaitForSingleObject(gate, uint32(supervisorGateTimeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("wait for daemon start gate: %w", err)
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("wait for daemon start gate: timed out after %s", supervisorGateTimeout)
	}
	if status != uint32(windows.WAIT_OBJECT_0) {
		return fmt.Errorf("wait for daemon start gate: unexpected status %d", status)
	}
	return nil
}

func RunSupervisor(cfg SupervisorConfig) error {
	resolved, err := cfg.resolve()
	if err != nil {
		return err
	}
	if !currentProcessElevated() {
		return fmt.Errorf("Windows daemon supervisor requires an elevated administrator token")
	}
	supervisorLock, err := acquireSupervisorLock(resolved.ConfigPath)
	if err != nil {
		return err
	}
	defer supervisorLock.Release()
	logWriter, err := NewRotatingWriter(resolved.LogFile, resolved.LogMaxSize, resolved.LogBackups)
	if err != nil {
		return fmt.Errorf("open supervisor log %s: %w", resolved.LogFile, err)
	}
	defer logWriter.Close()
	resolved.logWriter = logWriter
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("daemon supervisor started", "binary", resolved.BinaryPath, "config", resolved.ConfigPath)
	lockPath := instanceLockPath(resolved.ConfigPath)

	for {
		if err := guardExistingInstance(lockPath); err != nil {
			return fmt.Errorf("guard existing cc-connect instance: %w", err)
		}
		err := runSupervisedInstance(resolved)
		if err != nil {
			slog.Error("daemon supervisor: cc-connect exited", "error", err)
		} else {
			slog.Warn("daemon supervisor: cc-connect exited normally; restarting")
		}
		time.Sleep(resolved.RestartDelay)
	}
}

func guardExistingInstance(lockPath string) error {
	for {
		held, pid, err := inspectWindowsInstanceLock(lockPath)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
		if pid <= 0 {
			time.Sleep(supervisorLockPoll)
			continue
		}
		slog.Info("daemon supervisor: guarding existing cc-connect instance", "pid", pid)
		if err := adoptAndWaitForProcessTree(uint32(pid)); err != nil {
			return err
		}
	}
}

func inspectWindowsInstanceLock(lockPath string) (bool, int, error) {
	path, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return false, 0, fmt.Errorf("resolve instance lock path: %w", err)
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err == nil {
		windows.CloseHandle(handle)
		return false, 0, nil
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return false, 0, fmt.Errorf("inspect instance lock %s: %w", lockPath, err)
	}
	data, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		return true, 0, nil
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil || pid <= 0 {
		return true, 0, nil
	}
	return true, pid, nil
}

func adoptAndWaitForProcessTree(rootPID uint32) error {
	root, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, rootPID)
	if err != nil {
		if processGone(err) {
			return nil
		}
		return fmt.Errorf("open existing cc-connect pid %d: %w", rootPID, err)
	}
	defer windows.CloseHandle(root)
	watcher := newProcessTreeWatcher(rootPID)
	if err := watcher.capture(); err != nil {
		slog.Warn("daemon supervisor: initial process-tree snapshot was incomplete", "pid", rootPID, "error", err)
	}
	defer watcher.close()

	// Keep the handoff job non-destructive until every process has been
	// assigned. A process that is already in another (non-nestable) job makes
	// AssignProcessToJobObject return ERROR_ACCESS_DENIED; closing a partially
	// populated kill-on-close job in that case would terminate the live
	// instance before the fallback watcher can take over.
	job, err := newProcessJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	if err := assignExistingProcessTree(job, rootPID); err != nil {
		slog.Warn("daemon supervisor: existing process tree could not be fully adopted; using process-tree watcher", "pid", rootPID, "error", err)
		return waitAndCleanupProcessTree(root, job, watcher)
	}
	if err := enableKillOnClose(job); err != nil {
		slog.Warn("daemon supervisor: could not enable kill-on-close for adopted process; using process-tree watcher", "pid", rootPID, "error", err)
		return waitAndCleanupProcessTree(root, job, watcher)
	}
	slog.Info("daemon supervisor: adopted existing cc-connect process tree", "pid", rootPID)
	return waitAndCleanupProcessTree(root, job, watcher)
}

// waitAndCleanupProcessTree waits for the root to exit before terminating any
// descendants. The ordering is important for a handoff: an existing cc-connect
// process may be in a non-nestable Job Object, so closing a partially populated
// handoff job while it is still serving would be an unintentional interruption.
func waitAndCleanupProcessTree(root windows.Handle, job windows.Handle, watcher *processTreeWatcher) error {
	if err := watcher.wait(root); err != nil {
		return err
	}

	var cleanupErrs []error
	if job != 0 {
		cleanupErrs = append(cleanupErrs, terminateAndDrainJob(job))
	}
	if watcher != nil {
		cleanupErrs = append(cleanupErrs, watcher.terminateDescendants())
	}
	return errors.Join(cleanupErrs...)
}

func runSupervisedInstance(cfg SupervisorConfig) error {
	cmd := exec.Command(cfg.BinaryPath, "--config", cfg.ConfigPath)
	cmd.Dir = cfg.WorkDir
	cmd.Env = setWindowsEnv(os.Environ(), map[string]string{
		supervisedProcessEnv: "1",
		"CC_LOG_FILE":        "",
	})
	cmd.Stdout = cfg.logWriter
	cmd.Stderr = cfg.logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return runCommandInKillJob(cmd)
}

func runCommandInKillJob(cmd *exec.Cmd) error {
	job, err := newKillOnCloseJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	gateName := fmt.Sprintf("Local\\cc-connect-daemon-%d-%d", os.Getpid(), time.Now().UnixNano())
	gateNamePtr, err := windows.UTF16PtrFromString(gateName)
	if err != nil {
		return fmt.Errorf("create daemon start gate name: %w", err)
	}
	gate, err := windows.CreateEvent(nil, 1, 0, gateNamePtr)
	if err != nil {
		return fmt.Errorf("create daemon start gate: %w", err)
	}
	defer windows.CloseHandle(gate)

	baseEnv := cmd.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = setWindowsEnv(baseEnv, map[string]string{supervisorStartGateEnv: gateName})
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start supervised process: %w", err)
	}

	assigned := false
	defer func() {
		if !assigned && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()
	if err := assignProcessToJob(job, uint32(cmd.Process.Pid)); err != nil {
		return fmt.Errorf("assign supervised process pid %d to job: %w", cmd.Process.Pid, err)
	}
	assigned = true
	if err := windows.SetEvent(gate); err != nil {
		_ = terminateAndDrainJob(job)
		return fmt.Errorf("release supervised process start gate: %w", err)
	}

	waitErr := cmd.Wait()
	cleanupErr := terminateAndDrainJob(job)
	if waitErr != nil {
		waitErr = fmt.Errorf("supervised process wait: %w", waitErr)
	}
	return errors.Join(waitErr, cleanupErr)
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := newProcessJob()
	if err != nil {
		return 0, err
	}
	if err := enableKillOnClose(job); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func newProcessJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create process job: %w", err)
	}
	return job, nil
}

func enableKillOnClose(job windows.Handle) error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return fmt.Errorf("configure process job: %w", err)
	}
	return nil
}

func assignExistingProcessTree(job windows.Handle, rootPID uint32) error {
	children, err := snapshotProcessChildren()
	if err != nil {
		return err
	}
	if err := assignProcessToJob(job, rootPID); err != nil {
		return fmt.Errorf("pid %d: %w", rootPID, err)
	}
	assigned := map[uint32]bool{rootPID: true}
	for _, pid := range descendantPIDs(rootPID, children) {
		if err := assignProcessToJob(job, pid); err != nil && !processGone(err) {
			return fmt.Errorf("pid %d: %w", pid, err)
		}
		assigned[pid] = true
	}

	// Children created after the first snapshot inherit the job from the root.
	// A second snapshot closes the small race for children created just before
	// the root was assigned; ERROR_ACCESS_DENIED here means it already inherited.
	children, err = snapshotProcessChildren()
	if err != nil {
		return err
	}
	for _, pid := range descendantPIDs(rootPID, children) {
		if assigned[pid] {
			continue
		}
		if err := assignProcessToJob(job, pid); err != nil && !processGone(err) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("pid %d: %w", pid, err)
		}
	}
	return nil
}

func assignProcessToJob(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		pid,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return assignProcessToJobObject(job, process)
}

type trackedProcess struct {
	handle             windows.Handle
	terminateRequested bool
}

type processTreeWatcher struct {
	rootPID   uint32
	processes map[uint32]*trackedProcess
}

func newProcessTreeWatcher(rootPID uint32) *processTreeWatcher {
	return &processTreeWatcher{
		rootPID:   rootPID,
		processes: make(map[uint32]*trackedProcess),
	}
}

func (w *processTreeWatcher) wait(root windows.Handle) error {
	for {
		status, err := windows.WaitForSingleObject(root, uint32(supervisorFallbackPoll/time.Millisecond))
		if err != nil {
			return fmt.Errorf("wait for existing cc-connect pid %d: %w", w.rootPID, err)
		}
		if captureErr := w.capture(); captureErr != nil {
			slog.Warn("daemon supervisor: process-tree snapshot was incomplete", "pid", w.rootPID, "error", captureErr)
		}
		switch status {
		case uint32(windows.WAIT_OBJECT_0):
			return nil
		case uint32(windows.WAIT_TIMEOUT):
			continue
		default:
			return fmt.Errorf("wait for existing cc-connect pid %d: unexpected status %d", w.rootPID, status)
		}
	}
}

func (w *processTreeWatcher) capture() error {
	children, err := snapshotProcessChildren()
	if err != nil {
		return err
	}

	// Existing tracked processes remain roots for this snapshot. This catches a
	// grandchild when its direct parent exits between two polling intervals.
	roots := make([]uint32, 0, len(w.processes)+1)
	roots = append(roots, w.rootPID)
	for pid := range w.processes {
		roots = append(roots, pid)
	}
	candidates := descendantPIDsFromRoots(roots, children)

	var captureErrs []error
	for pid, tracked := range w.processes {
		status, waitErr := windows.WaitForSingleObject(tracked.handle, 0)
		if waitErr != nil {
			captureErrs = append(captureErrs, fmt.Errorf("inspect descendant pid %d: %w", pid, waitErr))
			continue
		}
		if status == uint32(windows.WAIT_OBJECT_0) {
			windows.CloseHandle(tracked.handle)
			delete(w.processes, pid)
		}
	}

	for _, pid := range candidates {
		if pid == 0 || pid == w.rootPID {
			continue
		}
		if _, ok := w.processes[pid]; ok {
			continue
		}
		handle, openErr := windows.OpenProcess(
			windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
			false,
			pid,
		)
		if openErr != nil {
			if !processGone(openErr) {
				captureErrs = append(captureErrs, fmt.Errorf("track descendant pid %d: %w", pid, openErr))
			}
			continue
		}
		status, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr != nil {
			windows.CloseHandle(handle)
			captureErrs = append(captureErrs, fmt.Errorf("inspect descendant pid %d: %w", pid, waitErr))
			continue
		}
		if status == uint32(windows.WAIT_OBJECT_0) {
			windows.CloseHandle(handle)
			continue
		}
		w.processes[pid] = &trackedProcess{handle: handle}
	}
	return errors.Join(captureErrs...)
}

func (w *processTreeWatcher) terminateDescendants() error {
	deadline := time.Now().Add(supervisorFallbackDrain)
	var cleanupErrs []error
	for {
		if err := w.capture(); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}

		alive := 0
		for pid, tracked := range w.processes {
			status, waitErr := windows.WaitForSingleObject(tracked.handle, 0)
			if waitErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("wait for descendant pid %d: %w", pid, waitErr))
				continue
			}
			if status == uint32(windows.WAIT_OBJECT_0) {
				windows.CloseHandle(tracked.handle)
				delete(w.processes, pid)
				continue
			}

			alive++
			if tracked.terminateRequested {
				continue
			}
			if err := windows.TerminateProcess(tracked.handle, 1); err != nil {
				status, waitErr = windows.WaitForSingleObject(tracked.handle, 0)
				if waitErr != nil || status != uint32(windows.WAIT_OBJECT_0) {
					cleanupErrs = append(cleanupErrs, fmt.Errorf("terminate descendant pid %d: %w", pid, err))
				}
			}
			tracked.terminateRequested = true
		}

		if alive == 0 {
			return errors.Join(cleanupErrs...)
		}
		if time.Now().After(deadline) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("%d descendant processes still active after %s", alive, supervisorFallbackDrain))
			return errors.Join(cleanupErrs...)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (w *processTreeWatcher) close() {
	for pid, tracked := range w.processes {
		windows.CloseHandle(tracked.handle)
		delete(w.processes, pid)
	}
}

func snapshotProcessChildren() (map[uint32][]uint32, error) {
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

func descendantPIDs(rootPID uint32, children map[uint32][]uint32) []uint32 {
	return descendantPIDsFromRoots([]uint32{rootPID}, children)
}

func descendantPIDsFromRoots(rootPIDs []uint32, children map[uint32][]uint32) []uint32 {
	visited := make(map[uint32]bool, len(rootPIDs))
	queue := make([]uint32, 0)
	for _, rootPID := range rootPIDs {
		if rootPID == 0 || visited[rootPID] {
			continue
		}
		visited[rootPID] = true
		queue = append(queue, children[rootPID]...)
	}
	result := make([]uint32, 0, len(queue))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		result = append(result, pid)
		queue = append(queue, children[pid]...)
	}
	return result
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func terminateAndDrainJob(job windows.Handle) error {
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return fmt.Errorf("terminate supervised process job: %w", err)
	}
	deadline := time.Now().Add(supervisorJobDrainTimeout)
	for {
		var info jobObjectBasicAccountingInformation
		if err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
			nil,
		); err != nil {
			return fmt.Errorf("query supervised process job: %w", err)
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("supervised process job still has %d active processes after %s", info.ActiveProcesses, supervisorJobDrainTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func setWindowsEnv(base []string, values map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range values {
		prefix := key + "="
		replaced := false
		for i, item := range result {
			if strings.EqualFold(strings.SplitN(item, "=", 2)[0], key) {
				result[i] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, prefix+value)
		}
	}
	return result
}

func processGone(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND)
}
