//go:build windows

package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"golang.org/x/sys/windows"
)

const restartTimeout = 90 * time.Second
const supervisorIdentityEnv = "CC_DAEMON_SUPERVISOR_IDENTITY"
const supervisorVersionEnv = "CC_DAEMON_SUPERVISOR_VERSION"
const supervisorHashEnv = "CC_DAEMON_SUPERVISOR_SHA256"
const supervisorConfigEnv = "CC_DAEMON_SUPERVISOR_CONFIG"

var errRestartProcessChanged = errors.New("process identity changed")

func restartProcessGone(err error) bool {
	return processGone(err) || errors.Is(err, errRestartProcessChanged)
}

func identifyProcess(pid uint32) (processIdentity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return processIdentity{}, err
	}
	defer windows.CloseHandle(h)
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return processIdentity{}, err
	}
	return processIdentity{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, nil
}

func openIdentifiedProcess(identity processIdentity) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, identity.PID)
	if err != nil {
		return 0, err
	}
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	if uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != identity.Created {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("%w (PID %d)", errRestartProcessChanged, identity.PID)
	}
	return h, nil
}

func processExited(handle windows.Handle) bool {
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == windows.WAIT_OBJECT_0
}

func currentSupervisor(config, version, digest string) (processIdentity, error) {
	var identity processIdentity
	if err := json.Unmarshal([]byte(os.Getenv(supervisorIdentityEnv)), &identity); err != nil || identity.PID == 0 {
		return identity, fmt.Errorf("legacy supervisor: a complete scheduled-task restart is required before managed restart/update")
	}
	abs, err := filepath.Abs(config)
	if err != nil {
		return identity, err
	}
	if !strings.EqualFold(filepath.Clean(abs), os.Getenv(supervisorConfigEnv)) {
		return identity, fmt.Errorf("supervisor configuration does not match this instance")
	}
	if version != "" && normalizeRestartVersion(version) != normalizeRestartVersion(os.Getenv(supervisorVersionEnv)) {
		return identity, fmt.Errorf("supervisor version %s differs from main process %s; complete scheduled-task restart required", os.Getenv(supervisorVersionEnv), version)
	}
	if digest != "" && digest != os.Getenv(supervisorHashEnv) {
		return identity, fmt.Errorf("supervisor executable differs from main process; complete scheduled-task restart required")
	}
	h, err := openIdentifiedProcess(identity)
	if err != nil {
		return identity, err
	}
	defer windows.CloseHandle(h)
	if processExited(h) {
		return identity, fmt.Errorf("supervisor already exited")
	}
	return identity, nil
}

func normalizeRestartVersion(s string) string { return strings.TrimPrefix(s, "fork-") }

// ValidateRestart fails before downloading/installing if this process cannot
// safely hand off to an independent controller. Never stop an unbound task.
func ValidateRestart(config string) error {
	if os.Getenv(supervisedProcessEnv) == "" {
		return nil
	}
	if _, err := currentSupervisor(config, "", ""); err != nil {
		return err
	}
	dir := restartDirectory(config)
	if _, err := os.Stat(dir); err == nil {
		var result restartResult
		if err := readRestartJSON(filepath.Join(dir, "result.json"), &result); err != nil {
			return fmt.Errorf("another restart is pending; inspect %s", dir)
		}
	}
	_, err := findRestartTask(uint32(os.Getpid()))
	return err
}

// Task matching uses the running ancestor's actual command arguments. It works
// with Python launchers as well as PowerShell; it does not rewrite their actions.
func findRestartTask(pid uint32) (restartTask, error) {
	script := fmt.Sprintf(`
$allProcesses = @(Get-CimInstance Win32_Process)
$ancestors = @()
$next = %d
$seen = @{}
while ($next -gt 0 -and -not $seen.ContainsKey($next)) {
 $seen[$next] = $true
 $p = $allProcesses | Where-Object ProcessId -eq $next | Select-Object -First 1
 if ($null -eq $p) { break }
 $ancestors += @{ pid=[int]$p.ProcessId; executable=$p.ExecutablePath; command=$p.CommandLine }
 $next = [int]$p.ParentProcessId
}
$tasks = @(Get-ScheduledTask | Where-Object State -eq 'Running' | ForEach-Object {
 @{ name=$_.TaskName; path=$_.TaskPath; principal=$_.Principal.UserId; logon=[string]$_.Principal.LogonType; actions=@($_.Actions | ForEach-Object { @{ execute=$_.Execute; arguments=$_.Arguments; directory=$_.WorkingDirectory } }) }
})
$processes = @($allProcesses | ForEach-Object { @{ pid=[int]$_.ProcessId; parent=[int]$_.ParentProcessId; supervisor=($_.CommandLine -match '(?:^|\s)_daemon-supervise(?:\s|$)') } })
@{ ancestors=$ancestors; tasks=$tasks; processes=$processes } | ConvertTo-Json -Depth 8 -Compress
`, pid)
	out, err := runPowerShell(script)
	if err != nil {
		return restartTask{}, fmt.Errorf("discover current scheduled task: %w", err)
	}
	var snapshot struct {
		Processes []struct {
			PID        uint32 `json:"pid"`
			Parent     uint32 `json:"parent"`
			Supervisor bool   `json:"supervisor"`
		} `json:"processes"`
		Ancestors []struct {
			PID        uint32 `json:"pid"`
			Executable string `json:"executable"`
			Command    string `json:"command"`
		} `json:"ancestors"`
		Tasks []struct {
			Name      string `json:"name"`
			Path      string `json:"path"`
			Principal string `json:"principal"`
			Logon     string `json:"logon"`
			Actions   []struct {
				Execute   string `json:"execute"`
				Arguments string `json:"arguments"`
				Directory string `json:"directory"`
			} `json:"actions"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		return restartTask{}, fmt.Errorf("decode scheduled-task binding: %w", err)
	}
	var matches []restartTask
	for _, task := range snapshot.Tasks {
		matched := false
		for _, action := range task.Actions {
			expected, err := windows.DecomposeCommandLine("task " + action.Arguments)
			if err != nil {
				continue
			}
			for _, ancestor := range snapshot.Ancestors {
				actual, err := windows.DecomposeCommandLine(ancestor.Command)
				if err != nil || len(actual) != len(expected) || len(actual) == 0 {
					continue
				}
				execute := strings.Trim(action.Execute, "\"")
				if filepath.IsAbs(execute) {
					if !strings.EqualFold(filepath.Clean(execute), filepath.Clean(ancestor.Executable)) {
						continue
					}
				} else if !strings.EqualFold(filepath.Base(execute), filepath.Base(ancestor.Executable)) {
					continue
				}
				equal := true
				for i := 1; i < len(actual); i++ {
					if actual[i] != expected[i] {
						equal = false
						break
					}
				}
				if equal {
					children := map[uint32][]uint32{}
					supervisors := map[uint32]bool{}
					for _, process := range snapshot.Processes {
						children[process.Parent] = append(children[process.Parent], process.PID)
						supervisors[process.PID] = process.Supervisor
					}
					count := 0
					if supervisors[ancestor.PID] {
						count++
					}
					for _, child := range descendantPIDs(ancestor.PID, children) {
						if supervisors[child] {
							count++
						}
					}
					if count != 1 || len(task.Actions) != 1 {
						return restartTask{}, fmt.Errorf("scheduled task does not exclusively own this supervisor (supervisors: %d, actions: %d)", count, len(task.Actions))
					}
					matched = true
				}
			}
		}
		if matched {
			fingerprint, err := taskFingerprint(task.Path, task.Name)
			if err != nil {
				return restartTask{}, err
			}
			matches = append(matches, restartTask{Name: task.Name, Path: task.Path, Fingerprint: fingerprint})
		}
	}
	if len(matches) != 1 {
		return restartTask{}, fmt.Errorf("cannot uniquely bind this instance to a running scheduled task (matches: %d); use an authorized complete task restart", len(matches))
	}
	return matches[0], nil
}

func taskFingerprint(path, name string) (string, error) {
	out, err := runPowerShell(fmt.Sprintf("Export-ScheduledTask -TaskPath %s -TaskName %s", powerShellLiteral(path), powerShellLiteral(name)))
	if err != nil {
		return "", fmt.Errorf("read scheduled-task definition: %w", err)
	}
	hash := sha256.Sum256([]byte(out))
	return hex.EncodeToString(hash[:]), nil
}

func checkRestartTask(task restartTask) error {
	fingerprint, err := taskFingerprint(task.Path, task.Name)
	if err != nil {
		return err
	}
	if fingerprint != task.Fingerprint {
		return fmt.Errorf("scheduled-task definition changed during restart; refusing to operate on it")
	}
	return nil
}

func PrepareRestart(config, dataDir, binary, version string) (*PreparedRestart, error) {
	if os.Getenv(supervisedProcessEnv) == "" {
		return nil, nil
	}
	config, err := filepath.Abs(config)
	if err != nil {
		return nil, err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	supervisor, err := currentSupervisor(config, "", "")
	if err != nil {
		return nil, err
	}
	main, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return nil, err
	}
	task, err := findRestartTask(main.PID)
	if err != nil {
		return nil, err
	}
	digest, err := core.ExecutableSHA256(binary)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	plan := restartPlan{ID: hex.EncodeToString(nonce[:]), Config: config, DataDir: dataDir, Binary: binary, Version: version, SHA256: digest, Task: task, Main: main, Supervisor: supervisor, Created: time.Now().UTC(), Timeout: restartTimeout}
	plan.ControllerBinary = binary
	if tx, err := core.PendingUpdate(binary); err != nil {
		return nil, err
	} else if tx != nil {
		backupSHA, err := core.ExecutableSHA256(tx.Backup())
		if err != nil {
			return nil, err
		}
		if backupSHA != tx.PreviousSHA256 {
			return nil, fmt.Errorf("restart controller backup checksum mismatch")
		}
		plan.ControllerBinary = tx.Backup()
		// A manual /restart can be retrying an already installed update after
		// controller preparation failed. The receipt owns the target version.
		if tx.Version != "" {
			plan.Version = tx.Version
		}
	}
	dir := restartDirectory(config)
	if err := core.CheckUpdatePath(dir); err != nil {
		return nil, err
	}
	if err := clearCompletedRestart(dir); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("another restart is pending or its directory is unavailable: %w", err)
	}
	plan.Created = time.Now().UTC()
	planPath := filepath.Join(dir, "plan.json")
	if err := writeRestartJSON(planPath, plan); err != nil {
		return nil, err
	}
	if err := launchRestartController(plan, planPath); err != nil {
		_ = writeRestartJSON(filepath.Join(dir, "result.json"), restartResult{ID: plan.ID, Error: err.Error()})
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var result restartResult
		if readRestartJSON(filepath.Join(dir, "result.json"), &result) == nil {
			return nil, fmt.Errorf("restart controller: %s", result.Error)
		}
		if readRestartJSON(filepath.Join(dir, "armed.json"), &result) == nil && result.ID == plan.ID {
			return &PreparedRestart{directory: dir, id: plan.ID}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	// No shutdown-ready record is written on failure. The controller times out
	// without stopping the task, so business processing can continue safely.
	return nil, fmt.Errorf("restart controller did not acknowledge within 30s; running instance retained")
}

func controllerTaskName(id string) string { return "cc-connect-restart-" + id }

func launchRestartController(plan restartPlan, path string) error {
	args := windows.ComposeCommandLine([]string{"_daemon-restart-controller", path})
	_, err := runPowerShell(fmt.Sprintf(`
$identity=[Security.Principal.WindowsIdentity]::GetCurrent()
$sid=$identity.User.Value
$kind='Interactive'
if ($sid -in @('S-1-5-18','S-1-5-19','S-1-5-20')) { $kind='ServiceAccount' }
$principal=New-ScheduledTaskPrincipal -UserId $sid -LogonType $kind -RunLevel Highest
$action=New-ScheduledTaskAction -Execute %s -Argument %s
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 10) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName %s -Action $action -Principal $principal -Settings $settings | Out-Null
Start-ScheduledTask -TaskName %s
`, powerShellLiteral(plan.ControllerBinary), powerShellLiteral(args), powerShellLiteral(controllerTaskName(plan.ID)), powerShellLiteral(controllerTaskName(plan.ID))))
	if err != nil {
		return fmt.Errorf("launch independent restart task: %w", err)
	}
	return nil
}

func clearCompletedRestart(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var result restartResult
	if err := readRestartJSON(filepath.Join(dir, "result.json"), &result); err != nil {
		return fmt.Errorf("unresolved restart metadata at %s: %w", dir, err)
	}
	var coordinator processIdentity
	if err := readRestartJSON(filepath.Join(dir, "controller.json"), &coordinator); err == nil {
		if h, err := openIdentifiedProcess(coordinator); err == nil {
			defer windows.CloseHandle(h)
			if !processExited(h) {
				status, err := windows.WaitForSingleObject(h, 30000)
				if err != nil || status != windows.WAIT_OBJECT_0 {
					return fmt.Errorf("previous restart controller is still running")
				}
			}
		} else if !restartProcessGone(err) {
			return fmt.Errorf("cannot verify previous controller exit: %w", err)
		}
	}
	allowed := map[string]bool{"plan.json": true, "armed.json": true, "shutdown-ready.json": true, "ready.json": true, "result.json": true, "controller.json": true, "startup.json": true}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !allowed[e.Name()] || e.IsDir() {
			return fmt.Errorf("unexpected restart artifact: %s", e.Name())
		}
		if err := core.CheckUpdatePath(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

// ConfirmRestartReady is called after all engine state is restored. The main
// process only reports success after the independent controller commits.
func ConfirmRestartReady(config, version, digest string) error {
	if os.Getenv(supervisedProcessEnv) == "" {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		tx, err := core.PendingUpdate(binary)
		if err != nil || tx == nil {
			return err
		}
		if normalizeRestartVersion(tx.Version) != normalizeRestartVersion(version) {
			return fmt.Errorf("installed version differs from running version")
		}
		return core.ActivateUpdate(binary, digest)
	}
	supervisor, err := currentSupervisor(config, version, digest)
	dir := restartDirectory(config)
	var plan restartPlan
	readErr := readRestartJSON(filepath.Join(dir, "plan.json"), &plan)
	if os.IsNotExist(readErr) {
		if err != nil {
			return err
		}
		return activateRunningUpdate(version, digest)
	}
	if readErr != nil {
		return readErr
	}
	var previous restartResult
	if readRestartJSON(filepath.Join(dir, "result.json"), &previous) == nil {
		if previous.Error != "" {
			return fmt.Errorf("previous restart failed: %s; %s", previous.Error, previous.Rollback)
		}
		if err != nil {
			return err
		}
		return activateRunningUpdate(version, digest)
	}
	main, idErr := identifyProcess(uint32(os.Getpid()))
	if idErr != nil {
		return idErr
	}
	ready := restartReady{ID: plan.ID, Version: version, SHA256: digest, Main: main, Supervisor: supervisor}
	if err != nil {
		ready.Error = err.Error()
	}
	if err := writeRestartJSON(filepath.Join(dir, "ready.json"), ready); err != nil {
		return err
	}
	deadline := plan.Created.Add(plan.Timeout * 2)
	for time.Now().Before(deadline) {
		var result restartResult
		if readRestartJSON(filepath.Join(dir, "result.json"), &result) == nil && result.ID == plan.ID {
			if result.Error != "" {
				return fmt.Errorf("restart was not activated: %s (%s)", result.Error, result.Rollback)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("restart activation confirmation timed out")
}

func activateRunningUpdate(version, digest string) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	tx, err := core.PendingUpdate(binary)
	if err != nil || tx == nil {
		return err
	}
	if normalizeRestartVersion(version) != normalizeRestartVersion(tx.Version) {
		return fmt.Errorf("running version does not match installed transaction")
	}
	return core.ActivateUpdate(binary, digest)
}

func RunRestartController(path string) (resultErr error) {
	if !currentProcessElevated() {
		return fmt.Errorf("restart controller requires the current instance's elevated identity")
	}
	var plan restartPlan
	if err := readRestartJSON(path, &plan); err != nil {
		return err
	}
	dir := restartDirectory(plan.Config)
	if filepath.Clean(path) != filepath.Join(dir, "plan.json") || len(plan.ID) != 32 || plan.Timeout < time.Second || plan.Timeout > 5*time.Minute || time.Since(plan.Created) > plan.Timeout {
		return fmt.Errorf("invalid or expired restart plan")
	}
	defer func() {
		_, _ = runPowerShell(fmt.Sprintf("Unregister-ScheduledTask -TaskName %s -Confirm:$false", powerShellLiteral(controllerTaskName(plan.ID))))
	}()
	result := restartResult{ID: plan.ID}
	defer func() {
		if resultErr != nil {
			result.Error = resultErr.Error()
		}
		if err := writeRestartJSON(filepath.Join(dir, "result.json"), result); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	identity, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return err
	}
	if err := writeRestartJSON(filepath.Join(dir, "controller.json"), identity); err != nil {
		return err
	}
	main, err := openIdentifiedProcess(plan.Main)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(main)
	supervisor, err := openIdentifiedProcess(plan.Supervisor)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(supervisor)
	if err := checkRestartTask(plan.Task); err != nil {
		return err
	}
	if err := writeRestartJSON(filepath.Join(dir, "armed.json"), restartResult{ID: plan.ID}); err != nil {
		return err
	}
	deadline := plan.Created.Add(plan.Timeout)
	for {
		var shutdown restartResult
		if readRestartJSON(filepath.Join(dir, "shutdown-ready.json"), &shutdown) == nil && shutdown.ID == plan.ID {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("business shutdown did not commit before timeout; original task retained")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := stopBoundTask(plan.Task, []windows.Handle{main, supervisor}, deadline); err != nil {
		return err
	}
	if err := startBoundTask(plan.Task); err != nil {
		result.Rollback = rollbackRestart(plan)
		return err
	}
	ready, err := waitRestartReady(plan, time.Now().Add(plan.Timeout))
	if err != nil {
		result.Rollback = rollbackRestart(plan)
		return err
	}
	if err := core.ActivateUpdate(plan.Binary, ready.SHA256); err != nil {
		result.Rollback = rollbackRestart(plan)
		return err
	}
	return nil
}

func stopBoundTask(task restartTask, handles []windows.Handle, deadline time.Time) error {
	if err := checkRestartTask(task); err != nil {
		return err
	}
	_, err := runPowerShell(fmt.Sprintf("Stop-ScheduledTask -TaskPath %s -TaskName %s", powerShellLiteral(task.Path), powerShellLiteral(task.Name)))
	if err != nil {
		return fmt.Errorf("stop current scheduled task: %w", err)
	}
	for time.Now().Before(deadline) {
		all := true
		for _, h := range handles {
			all = all && processExited(h)
		}
		state, err := runPowerShell(fmt.Sprintf("(Get-ScheduledTask -TaskPath %s -TaskName %s).State", powerShellLiteral(task.Path), powerShellLiteral(task.Name)))
		if err != nil {
			return err
		}
		if all && !strings.EqualFold(strings.TrimSpace(state), "Running") {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("stop timeout: old task/processes did not exit; replacement not started")
}

func startBoundTask(task restartTask) error {
	if err := checkRestartTask(task); err != nil {
		return err
	}
	_, err := runPowerShell(fmt.Sprintf("Start-ScheduledTask -TaskPath %s -TaskName %s", powerShellLiteral(task.Path), powerShellLiteral(task.Name)))
	if err != nil {
		return fmt.Errorf("start current scheduled task: %w", err)
	}
	return nil
}

func waitRestartReady(plan restartPlan, deadline time.Time) (*restartReady, error) {
	for time.Now().Before(deadline) {
		var ready restartReady
		if readRestartJSON(filepath.Join(restartDirectory(plan.Config), "ready.json"), &ready) == nil && ready.ID == plan.ID {
			if ready.Error != "" {
				return nil, errors.New(ready.Error)
			}
			if normalizeRestartVersion(ready.Version) != normalizeRestartVersion(plan.Version) || ready.SHA256 != plan.SHA256 || ready.Main == plan.Main || ready.Supervisor == plan.Supervisor {
				return nil, fmt.Errorf("new instance version, executable or process identity mismatch")
			}
			for _, id := range []processIdentity{ready.Main, ready.Supervisor} {
				h, err := openIdentifiedProcess(id)
				if err != nil {
					return nil, err
				}
				exited := processExited(h)
				windows.CloseHandle(h)
				if exited {
					return nil, fmt.Errorf("new instance exited before activation")
				}
			}
			return &ready, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("new instance readiness timed out")
}

func rollbackRestart(plan restartPlan) string {
	// Readiness records carry PID plus creation time, never an unverified PID.
	var ready restartReady
	var handles []windows.Handle
	if readRestartJSON(filepath.Join(restartDirectory(plan.Config), "ready.json"), &ready) == nil && ready.ID == plan.ID {
		for _, id := range []processIdentity{ready.Main, ready.Supervisor} {
			if h, err := openIdentifiedProcess(id); err == nil {
				handles = append(handles, h)
				defer windows.CloseHandle(h)
			} else if !restartProcessGone(err) {
				return "rollback not attempted: cannot verify replacement process: " + err.Error()
			}
		}
	}
	var started processIdentity
	if readRestartJSON(filepath.Join(restartDirectory(plan.Config), "startup.json"), &started) == nil {
		if h, err := openIdentifiedProcess(started); err == nil {
			handles = append(handles, h)
			defer windows.CloseHandle(h)
		} else if !restartProcessGone(err) {
			return "rollback not attempted: cannot verify replacement supervisor: " + err.Error()
		}
	}
	if err := stopBoundTask(plan.Task, handles, time.Now().Add(plan.Timeout)); err != nil {
		return "rollback not attempted: " + err.Error()
	}
	tx, err := core.PendingUpdate(plan.Binary)
	if err != nil {
		return "rollback inspection failed: " + err.Error()
	}
	if tx == nil {
		return "restart failed; no update transaction to roll back"
	}
	if err := core.RollbackUpdate(plan.Binary); err != nil {
		return "rollback failed: " + err.Error()
	}
	// Transaction-based rollback is created by a version that already supports
	// failed-activation notifications. Preserve the destination and report failure
	// when the previous executable reconnects; never leave a generic success.
	notifyPath := filepath.Join(plan.DataDir, "run", "restart_notify")
	if err := core.CheckUpdatePath(notifyPath); err != nil {
		return "binary restored, notification path rejected: " + err.Error()
	}
	if data, err := os.ReadFile(notifyPath); err == nil {
		var notify core.RestartRequest
		if err := json.Unmarshal(data, &notify); err != nil {
			return "binary restored, invalid restart notification: " + err.Error()
		}
		notify.Error = fmt.Sprintf("update to %s did not activate; previous executable restored", plan.Version)
		if err := core.SaveRestartNotify(plan.DataDir, notify); err != nil {
			return "binary restored, failure notification could not be saved: " + err.Error()
		}
	} else if !os.IsNotExist(err) {
		return "binary restored, restart notification could not be read: " + err.Error()
	}
	if err := startBoundTask(plan.Task); err != nil {
		return "binary restored; old instance start failed: " + err.Error()
	}
	return "previous binary restored and original task started; inspect its health before retrying"
}

func supervisorRestartEnvironment(cfg SupervisorConfig) (map[string]string, error) {
	id, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	digest, err := core.ExecutableSHA256(cfg.BinaryPath)
	if err != nil {
		return nil, err
	}
	return map[string]string{supervisorIdentityEnv: string(data), supervisorVersionEnv: cfg.Version, supervisorHashEnv: digest, supervisorConfigEnv: cfg.ConfigPath}, nil
}

// Kept separate for tests that need a supervised child without an installed task.
func Supervised() bool { return os.Getenv(supervisedProcessEnv) != "" }

func recordSupervisorStartup(cfg SupervisorConfig) error {
	dir := restartDirectory(cfg.ConfigPath)
	var plan restartPlan
	if err := readRestartJSON(filepath.Join(dir, "plan.json"), &plan); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var result restartResult
	if readRestartJSON(filepath.Join(dir, "result.json"), &result) == nil {
		return nil
	}
	id, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return err
	}
	if id == plan.Supervisor {
		return fmt.Errorf("old supervisor cannot satisfy restart startup")
	}
	return writeRestartJSON(filepath.Join(dir, "startup.json"), id)
}

func waitForCommittedRestart(cfg SupervisorConfig) error {
	dir := restartDirectory(cfg.ConfigPath)
	var plan restartPlan
	if err := readRestartJSON(filepath.Join(dir, "plan.json"), &plan); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	id, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return err
	}
	if plan.Supervisor != id {
		return nil
	}
	var ready restartResult
	if readRestartJSON(filepath.Join(dir, "shutdown-ready.json"), &ready) != nil || ready.ID != plan.ID {
		return nil
	}
	// The old supervisor must not race the independent controller by spawning
	// another business child between committed shutdown and task stop.
	for time.Now().Before(plan.Created.Add(plan.Timeout * 2)) {
		var result restartResult
		if readRestartJSON(filepath.Join(dir, "result.json"), &result) == nil {
			return fmt.Errorf("committed task restart ended: %s", result.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("committed restart controller did not stop the old supervisor before timeout")
}
