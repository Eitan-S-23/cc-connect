//go:build windows

package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"golang.org/x/sys/windows"
)

const fixtureMarker = "\ncc-connect-isolated-test-version="

// The test executable doubles as an isolated bot. It never loads config.toml,
// creates an agent, or connects to a messaging platform. The production task
// controller, supervisor, Job Object and updater all execute unchanged.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "_daemon-restart-controller":
			if len(os.Args) != 3 {
				os.Exit(2)
			}
			err = RunRestartController(os.Args[2])
		case "_daemon-supervise":
			if len(os.Args) != 4 || os.Args[2] != "--config" {
				os.Exit(2)
			}
			exe, e := os.Executable()
			if e != nil {
				os.Exit(2)
			}
			version, e := fixtureVersion(exe)
			if e != nil {
				os.Exit(2)
			}
			if version == "BROKEN" {
				os.Exit(45)
			}
			config := os.Args[3]
			err = RunSupervisor(SupervisorConfig{BinaryPath: exe, ConfigPath: config, Version: version, WorkDir: filepath.Dir(config), LogFile: filepath.Join(filepath.Dir(config), "supervisor.log"), RestartDelay: time.Second})
		case "--config":
			if len(os.Args) != 3 {
				os.Exit(2)
			}
			err = runRestartFixture(os.Args[2])
		default:
			os.Exit(m.Run())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixtureVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	i := bytes.LastIndex(data, []byte(fixtureMarker))
	if i < 0 {
		return "", fmt.Errorf("not an isolated fixture executable")
	}
	return strings.TrimSpace(string(data[i+len(fixtureMarker):])), nil
}

type fixtureRequest struct{ Action, Image, Version string }

func runRestartFixture(config string) error {
	if err := WaitForSupervisorStartGate(); err != nil {
		return err
	}
	if err := RequireSupervisedElevation(); err != nil {
		return err
	}
	lockPath, err := windows.UTF16PtrFromString(instanceLockPath(config))
	if err != nil {
		return err
	}
	lock, err := windows.CreateFile(lockPath, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("fixture duplicate instance: %w", err)
	}
	defer windows.CloseHandle(lock)
	var written uint32
	if err := windows.WriteFile(lock, []byte(fmt.Sprint(os.Getpid())), &written, nil); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	version, err := fixtureVersion(exe)
	if err != nil {
		return err
	}
	digest, err := core.ExecutableSHA256(exe)
	if err != nil {
		return err
	}
	activation := ConfirmRestartReady(config, version, digest)
	main, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		return err
	}
	supervisor, err := currentSupervisor(config, version, digest)
	if err != nil && activation == nil {
		return err
	}
	state := restartReady{Version: version, SHA256: digest, Main: main, Supervisor: supervisor}
	if activation != nil {
		state.Error = activation.Error()
	}
	dir := filepath.Dir(config)
	if err := writeRestartJSON(filepath.Join(dir, "fixture-ready.json"), state); err != nil {
		return err
	}
	for {
		var request fixtureRequest
		path := filepath.Join(dir, "request.json")
		if readRestartJSON(path, &request) != nil {
			time.Sleep(30 * time.Millisecond)
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if request.Action == "exit" {
			return nil
		}
		err := ValidateRestart(config)
		if err == nil && request.Action == "upgrade" {
			var data []byte
			data, err = os.ReadFile(request.Image)
			if err == nil {
				_, err = core.InstallUpdateTransaction(exe, data, request.Version)
			}
		}
		if err == nil {
			requested := request.Version
			if requested == "" {
				requested = version
			}
			var prepared *PreparedRestart
			prepared, err = PrepareRestart(config, dir, exe, requested)
			if err == nil && prepared == nil {
				err = fmt.Errorf("fixture was not supervised")
			}
			if err == nil {
				err = prepared.Commit()
			}
			if err == nil {
				return nil
			}
		}
		if err := writeRestartJSON(filepath.Join(dir, "fixture-error.json"), restartResult{Error: err.Error()}); err != nil {
			return err
		}
	}
}

func waitFixture(t *testing.T, path string, predicate func(restartReady) bool) restartReady {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		var ready restartReady
		if readRestartJSON(path, &ready) == nil && predicate(ready) {
			return ready
		}
		var failure restartResult
		if readRestartJSON(filepath.Join(filepath.Dir(path), "fixture-error.json"), &failure) == nil {
			t.Fatal("fixture command failed:", failure.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, name := range []string{"supervisor.log", ".fixture.json.restart/result.json", ".fixture.json.restart/plan.json"} {
		data, _ := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		t.Log(name, string(data))
	}
	t.Fatal("timed out waiting for isolated task")
	return restartReady{}
}

func waitFixtureProcessesExit(t *testing.T, old restartReady) {
	t.Helper()
	for _, id := range []processIdentity{old.Main, old.Supervisor} {
		h, err := openIdentifiedProcess(id)
		if err != nil {
			continue
		}
		status, err := windows.WaitForSingleObject(h, 5000)
		windows.CloseHandle(h)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("old process did not exit: %+v (%d, %v)", id, status, err)
		}
	}
}

func TestWindowsTaskRestartContinuousUpgrades(t *testing.T) {
	if os.Getenv("CC_WINDOWS_TASK_TESTS") != "1" {
		t.Skip("requires an explicitly isolated Windows task test runner")
	}
	if !currentProcessElevated() {
		t.Fatal("Windows integration runner must be elevated; do not silently skip")
	}
	for _, launcher := range []string{"powershell", "python"} {
		t.Run(launcher, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "fixture.json")
			binary := filepath.Join(dir, "bot.exe")
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			image, err := os.ReadFile(self)
			if err != nil {
				t.Fatal(err)
			}
			images := map[string]string{}
			for _, version := range []string{"A", "B", "C", "D", "BROKEN"} {
				path := filepath.Join(dir, version+".exe")
				data := append(append([]byte(nil), image...), []byte(fixtureMarker+version+"\n")...)
				if err := os.WriteFile(path, data, 0o700); err != nil {
					t.Fatal(err)
				}
				images[version] = path
			}
			initial, _ := os.ReadFile(images["A"])
			if err := os.WriteFile(binary, initial, 0o700); err != nil {
				t.Fatal(err)
			}
			preserved := map[string]string{"fixture.json": "isolated fixture, never a live config", "sessions.json": "conversation history", "cron.json": "scheduled state"}
			for name, content := range preserved {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			taskName := fmt.Sprintf("cc-connect-isolated-%d-%d", os.Getpid(), time.Now().UnixNano())
			var execute, args string
			if launcher == "powershell" {
				execute = "powershell.exe"
				script := filepath.Join(dir, "launcher.ps1")
				if err := os.WriteFile(script, []byte(fmt.Sprintf("& %s '_daemon-supervise' '--config' %s\nexit $LASTEXITCODE\n", powerShellLiteral(binary), powerShellLiteral(config))), 0o600); err != nil {
					t.Fatal(err)
				}
				args = windows.ComposeCommandLine([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script})
			} else {
				execute, err = exec.LookPath("python.exe")
				if err != nil {
					t.Fatal("Python launcher regression requires Python", err)
				}
				script := filepath.Join(dir, "launcher.py")
				binaryJSON, _ := json.Marshal(binary)
				configJSON, _ := json.Marshal(config)
				code := fmt.Sprintf("import subprocess,sys\nsys.exit(subprocess.call([%s, '_daemon-supervise', '--config', %s]))\n", binaryJSON, configJSON)
				if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
					t.Fatal(err)
				}
				args = windows.ComposeCommandLine([]string{"-B", script})
			}
			_, err = runPowerShell(fmt.Sprintf(`
$action=New-ScheduledTaskAction -Execute %s -Argument %s
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName %s -Action $action -Principal $principal -Settings $settings | Out-Null
Start-ScheduledTask -TaskName %s
`, powerShellLiteral(execute), powerShellLiteral(args), powerShellLiteral(taskName), powerShellLiteral(taskName)))
			if err != nil {
				t.Fatal("create isolated task:", err)
			}
			t.Cleanup(func() {
				_, _ = runPowerShell(fmt.Sprintf("Stop-ScheduledTask -TaskName %s; Unregister-ScheduledTask -TaskName %s -Confirm:$false", powerShellLiteral(taskName), powerShellLiteral(taskName)))
				var plan restartPlan
				if readRestartJSON(filepath.Join(restartDirectory(config), "plan.json"), &plan) == nil {
					_, _ = runPowerShell(fmt.Sprintf("$t=Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue; if($t){ Stop-ScheduledTask -TaskName %s; Unregister-ScheduledTask -TaskName %s -Confirm:$false }", powerShellLiteral(controllerTaskName(plan.ID)), powerShellLiteral(controllerTaskName(plan.ID)), powerShellLiteral(controllerTaskName(plan.ID))))
				}
			})
			readyPath := filepath.Join(dir, "fixture-ready.json")
			current := waitFixture(t, readyPath, func(r restartReady) bool { return r.Version == "A" })
			for _, step := range []fixtureRequest{{Action: "restart"}, {Action: "upgrade", Image: images["B"], Version: "B"}, {Action: "upgrade", Image: images["C"], Version: "C"}, {Action: "restart"}, {Action: "upgrade", Image: images["D"], Version: "D"}} {
				old := current
				if err := writeRestartJSON(filepath.Join(dir, "request.json"), step); err != nil {
					t.Fatal(err)
				}
				current = waitFixture(t, readyPath, func(r restartReady) bool { return r.Main != old.Main })
				if current.Error != "" {
					t.Fatal(current.Error)
				}
				if current.Supervisor == old.Supervisor {
					t.Fatal("restart left old supervisor running")
				}
				if step.Version != "" && current.Version != step.Version {
					t.Fatal("wrong running version", current.Version)
				}
				waitFixtureProcessesExit(t, old)
				if second, err := acquireSupervisorLock(config); err == nil {
					second.Release()
					t.Fatal("new supervisor did not own the single-instance lock")
				}
				held, pid, err := inspectWindowsInstanceLock(instanceLockPath(config))
				if err != nil || !held || uint32(pid) != current.Main.PID {
					t.Fatalf("business lock mismatch: %v %d %v", held, pid, err)
				}
				for name, want := range preserved {
					got, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || string(got) != want {
						t.Fatalf("state changed: %s", name)
					}
				}
				// Wait for the controller's actual process exit before another command.
				var controller processIdentity
				if err := readRestartJSON(filepath.Join(restartDirectory(config), "controller.json"), &controller); err != nil {
					t.Fatal(err)
				}
				if h, err := openIdentifiedProcess(controller); err == nil {
					status, e := windows.WaitForSingleObject(h, 30000)
					windows.CloseHandle(h)
					if e != nil || status != windows.WAIT_OBJECT_0 {
						t.Fatal("controller did not exit", status, e)
					}
				}
			}
			t.Log("real task: restart, A -> B -> C, restart, C -> D; old processes exited, one instance retained, state preserved")
			if launcher == "powershell" {
				old := current
				if err := writeRestartJSON(filepath.Join(dir, "request.json"), fixtureRequest{Action: "upgrade", Image: images["BROKEN"], Version: "BROKEN"}); err != nil {
					t.Fatal(err)
				}
				recovered := waitFixture(t, readyPath, func(r restartReady) bool { return r.Main != old.Main && r.Version == "D" })
				if recovered.Error == "" {
					t.Fatal("failed startup reported successful activation")
				}
				waitFixtureProcessesExit(t, old)
				if version, err := fixtureVersion(binary); err != nil || version != "D" {
					t.Fatal("failed startup did not restore D", version, err)
				}
				waitFixtureControllerExit(t, config)
				t.Log("unstartable next supervisor: timed out, restored D, failure preserved in receipt")
			}
		})
	}
}

func TestWindowsLegacySupervisorCannotReportActivation(t *testing.T) {
	t.Setenv(supervisedProcessEnv, "1")
	t.Setenv(supervisorIdentityEnv, "")
	if err := ValidateRestart(filepath.Join(t.TempDir(), "config.toml")); err == nil || !strings.Contains(err.Error(), "legacy supervisor") {
		t.Fatalf("legacy supervisor accepted: %v", err)
	}
	if err := ConfirmRestartReady(filepath.Join(t.TempDir(), "config.toml"), "B", "digest"); err == nil {
		t.Fatal("unverified supervisor reported activation")
	}
}

func TestWindowsRestartRejectsReusedPID(t *testing.T) {
	id, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	id.Created++
	if h, err := openIdentifiedProcess(id); err == nil {
		windows.CloseHandle(h)
		t.Fatal("PID without matching creation time accepted")
	}
}

func TestWindowsRestartStopTimeoutDoesNotStartReplacement(t *testing.T) {
	original := runPowerShell
	t.Cleanup(func() { runPowerShell = original })
	starts := 0
	runPowerShell = func(script string) (string, error) {
		if strings.Contains(script, "Export-ScheduledTask") {
			return "isolated task", nil
		}
		if strings.Contains(script, "Start-ScheduledTask") {
			starts++
		}
		return "Running", nil
	}
	fingerprint, err := taskFingerprint("\\", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	id, err := identifyProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	h, err := openIdentifiedProcess(id)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	err = stopBoundTask(restartTask{Name: "fixture", Path: "\\", Fingerprint: fingerprint}, []windows.Handle{h}, time.Now().Add(100*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), "stop timeout") || starts != 0 {
		t.Fatalf("stop timeout was hidden: %v, starts %d", err, starts)
	}
	if processExited(h) {
		t.Fatal("timed-out stop killed an unverified process")
	}
}

func TestWindowsRestartRefusesChangedTaskAndReportsStartFailure(t *testing.T) {
	original := runPowerShell
	t.Cleanup(func() { runPowerShell = original })
	runPowerShell = func(string) (string, error) { return "old definition", nil }
	fingerprint, err := taskFingerprint("\\", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	task := restartTask{Name: "fixture", Path: "\\", Fingerprint: fingerprint}
	runPowerShell = func(string) (string, error) { return "changed definition", nil }
	if err := startBoundTask(task); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatal("changed task accepted", err)
	}
	runPowerShell = func(script string) (string, error) {
		if strings.Contains(script, "Export-ScheduledTask") {
			return "old definition", nil
		}
		return "", os.ErrPermission
	}
	if err := startBoundTask(task); err == nil || !strings.Contains(err.Error(), "start current") {
		t.Fatal("start failure hidden", err)
	}
}

func TestWindowsLegacyReleaseToFixedReleaseRequiresCompleteRestart(t *testing.T) {
	if os.Getenv("CC_WINDOWS_TASK_TESTS") != "1" {
		t.Skip("requires isolated task runner")
	}
	legacyPath := os.Getenv("CC_LEGACY_SUPERVISOR_FIXTURE")
	if legacyPath == "" {
		t.Fatal("real baseline supervisor fixture missing")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "fixture.json")
	binary := filepath.Join(dir, "bot.exe")
	if err := os.WriteFile(config, []byte("isolated legacy transition"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	fixedA := append(append([]byte(nil), image...), []byte(fixtureMarker+"A\n")...)
	fixedB := append(append([]byte(nil), image...), []byte(fixtureMarker+"B\n")...)
	imageB := filepath.Join(dir, "B.exe")
	if err := os.WriteFile(imageB, fixedB, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "launcher.ps1")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("& %s '_daemon-supervise' '--config' %s\nexit $LASTEXITCODE\n", powerShellLiteral(binary), powerShellLiteral(config))), 0o600); err != nil {
		t.Fatal(err)
	}
	taskName := fmt.Sprintf("cc-connect-legacy-isolated-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := windows.ComposeCommandLine([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script})
	_, err = runPowerShell(fmt.Sprintf(`
$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument %s
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName %s -Action $action -Principal $principal -Settings $settings | Out-Null
Start-ScheduledTask -TaskName %s
`, powerShellLiteral(args), powerShellLiteral(taskName), powerShellLiteral(taskName)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = runPowerShell(fmt.Sprintf("Stop-ScheduledTask -TaskName %s; Unregister-ScheduledTask -TaskName %s -Confirm:$false", powerShellLiteral(taskName), powerShellLiteral(taskName)))
	})
	readyPath := filepath.Join(dir, "fixture-ready.json")
	old := waitFixture(t, readyPath, func(r restartReady) bool { return r.Version == "LEGACY" })
	// The old updater's actual filesystem operations; its mapped-file failure
	// is separately proved against the unchanged baseline regression.
	if err := os.Rename(binary, binary+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, fixedA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy-exit"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mixed := waitFixture(t, readyPath, func(r restartReady) bool { return r.Version == "A" })
	if !strings.Contains(mixed.Error, "legacy supervisor") {
		t.Fatal("new main claimed activation under real old supervisor", mixed)
	}
	oldSupervisor, err := openIdentifiedProcess(old.Supervisor)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(oldSupervisor)
	if processExited(oldSupervisor) {
		t.Fatal("baseline supervisor unexpectedly restarted itself")
	}
	if err := os.Remove(binary + ".old"); err == nil {
		t.Fatal("baseline supervisor did not hold old image")
	}
	fingerprint, err := taskFingerprint("\\", taskName)
	if err != nil {
		t.Fatal(err)
	}
	task := restartTask{Name: taskName, Path: "\\", Fingerprint: fingerprint}
	main, err := openIdentifiedProcess(mixed.Main)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(main)
	if err := stopBoundTask(task, []windows.Handle{main, oldSupervisor}, time.Now().Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := startBoundTask(task); err != nil {
		t.Fatal(err)
	}
	active := waitFixture(t, readyPath, func(r restartReady) bool { return r.Main != mixed.Main && r.Version == "A" })
	if active.Error != "" {
		t.Fatal(active.Error)
	}
	if err := writeRestartJSON(filepath.Join(dir, "request.json"), fixtureRequest{Action: "upgrade", Image: imageB, Version: "B"}); err != nil {
		t.Fatal(err)
	}
	next := waitFixture(t, readyPath, func(r restartReady) bool { return r.Version == "B" })
	if next.Error != "" {
		t.Fatal(next.Error)
	}
	waitFixtureProcessesExit(t, active)
	if _, err := os.Stat(binary + ".old"); err != nil {
		t.Fatal("legacy backup was unexpectedly deleted", err)
	}
	waitFixtureControllerExit(t, config)
	t.Log("real e2b9795 supervisor -> fixed A (activation withheld) -> authorized full task restart -> fixed B upgrade succeeded without deleting legacy .old")
}

func waitFixtureControllerExit(t *testing.T, config string) {
	t.Helper()
	var controller processIdentity
	if err := readRestartJSON(filepath.Join(restartDirectory(config), "controller.json"), &controller); err != nil {
		t.Fatal(err)
	}
	h, err := openIdentifiedProcess(controller)
	if err != nil {
		if restartProcessGone(err) {
			return
		}
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, 30000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("controller still active: %d %v", status, err)
	}
}
