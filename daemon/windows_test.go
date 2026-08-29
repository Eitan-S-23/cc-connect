//go:build windows

package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestStrictPowerShellStopsOnCmdletErrors(t *testing.T) {
	script := strictPowerShell("Write-Output 'ok'")
	if !strings.HasPrefix(script, "$ErrorActionPreference = 'Stop'\n") {
		t.Fatalf("strictPowerShell() missing stop prelude:\n%s", script)
	}
	if !strings.Contains(script, "Write-Output 'ok'") {
		t.Fatalf("strictPowerShell() missing original script:\n%s", script)
	}
}

func TestBuildWindowsTaskScript(t *testing.T) {
	cfg := Config{
		BinaryPath: `C:\Program Files\cc-connect\cc-connect.exe`,
		WorkDir:    `C:\Users\me\.cc-connect`,
		LogFile:    `C:\Users\me\.cc-connect\logs\cc-connect.log`,
		LogMaxSize: 10 * 1024 * 1024,
		EnvPATH:    `C:\Program Files\nodejs;C:\Users\me\AppData\Local\Programs`,
		EnvExtra: map[string]string{
			"HTTPS_PROXY": "http://127.0.0.1:7890",
			"http_proxy":  "http://127.0.0.1:7890",
		},
	}

	script := buildWindowsTaskScript(cfg)
	for _, want := range []string{
		`$env:CC_LOG_FILE = 'C:\Users\me\.cc-connect\logs\cc-connect.log'`,
		`$env:CC_LOG_MAX_SIZE = '10485760'`,
		`$env:PATH = 'C:\Program Files\nodejs;C:\Users\me\AppData\Local\Programs'`,
		`$env:HTTPS_PROXY = 'http://127.0.0.1:7890'`,
		`$env:http_proxy = 'http://127.0.0.1:7890'`,
		`[Security.Principal.WindowsIdentity]::GetCurrent()`,
		`[Security.Principal.WindowsBuiltInRole]::Administrator`,
		`Set-Location -LiteralPath 'C:\Users\me\.cc-connect'`,
		`& 'C:\Program Files\cc-connect\cc-connect.exe' '_daemon-supervise' '--config' 'C:\Users\me\.cc-connect\config.toml'`,
		`exit $LASTEXITCODE`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `while ($true)`) {
		t.Fatalf("PowerShell launcher must delegate restart ownership to the Go supervisor:\n%s", script)
	}
}

func TestWindowsTaskActionRunsHidden(t *testing.T) {
	got := windowsTaskAction(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`)
	for _, want := range []string{
		`powershell.exe`,
		`-WindowStyle Hidden`,
		`-NoProfile`,
		`-NonInteractive`,
		`-ExecutionPolicy Bypass`,
		`-File "C:\Users\me\.cc-connect\cc-connect-daemon.ps1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowsTaskAction() missing %q: %q", want, got)
		}
	}
}

func TestWindowsTaskCreateUsesHighestInteractivePrincipal(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })

	var script string
	runPowerShell = func(s string) (string, error) {
		script = s
		return "", nil
	}

	if err := createWindowsTask(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`); err != nil {
		t.Fatalf("createWindowsTask() error = %v", err)
	}
	for _, want := range []string{
		`[Security.Principal.WindowsIdentity]::GetCurrent().Name`,
		`New-ScheduledTaskTrigger -AtLogOn -User $accountId`,
		`New-ScheduledTaskAction`,
		`Register-ScheduledTask`,
		`-UserId $accountId`,
		`-LogonType Interactive`,
		`-RunLevel Highest`,
		`New-ScheduledTaskSettingsSet`,
		`-RestartCount 999`,
		`-RestartInterval (New-TimeSpan -Minutes 1)`,
		`-ExecutionTimeLimit ([TimeSpan]::Zero)`,
		`-MultipleInstances IgnoreNew`,
		`-StartWhenAvailable`,
		`-AllowStartIfOnBatteries`,
		`-DontStopIfGoingOnBatteries`,
		`-WindowStyle Hidden`,
		`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("create script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `-RunLevel Limited`) {
		t.Fatalf("create script unexpectedly uses limited privileges:\n%s", script)
	}
}

func TestWindowsTaskMatchesActionRequiresExactAction(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })

	var script string
	runPowerShell = func(s string) (string, error) {
		script = s
		return "true", nil
	}

	if !windowsTaskMatchesAction(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`) {
		t.Fatal("windowsTaskMatchesAction() = false, want true")
	}
	for _, want := range []string{
		`$expectedArgs = '-WindowStyle Hidden -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "C:\Users\me\.cc-connect\cc-connect-daemon.ps1"'`,
		`$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value`,
		`([Security.Principal.NTAccount]$task.Principal.UserId).Translate([Security.Principal.SecurityIdentifier]).Value`,
		`$taskSid -ne $currentSid`,
		`$task.Principal.RunLevel -ne 'Highest'`,
		`$task.Principal.LogonType -ne 'Interactive'`,
		`$task.Settings.MultipleInstances -ne 'IgnoreNew'`,
		`$task.Settings.ExecutionTimeLimit -ne 'PT0S'`,
		`$task.Settings.RestartCount -ne 999`,
		`$task.Settings.RestartInterval -ne 'PT1M'`,
		`$action.Execute -ieq 'powershell.exe'`,
		`$action.Arguments -eq $expectedArgs`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("reuse check script missing %q:\n%s", want, script)
		}
	}
}

func TestPowerShellLiteralEscapesSingleQuotes(t *testing.T) {
	got := powerShellLiteral(`C:\Users\O'Brien\.cc-connect`)
	want := `'C:\Users\O''Brien\.cc-connect'`
	if got != want {
		t.Fatalf("powerShellLiteral() = %q, want %q", got, want)
	}
}

func TestBuildWindowsTaskScript_DropsInvalidEnvName(t *testing.T) {
	cfg := Config{
		BinaryPath: "x", WorkDir: "y", LogFile: "l", LogMaxSize: 1, EnvPATH: "p",
		EnvExtra: map[string]string{"FOO BAR": "v", "OK": "ok"},
	}
	script := buildWindowsTaskScript(cfg)
	if strings.Contains(script, "FOO BAR") {
		t.Errorf("invalid env name leaked: %s", script)
	}
	if !strings.Contains(script, "$env:OK = 'ok'") {
		t.Errorf("valid env missing: %s", script)
	}
}

func TestBuildWindowsTaskScript_DropsEmptyValue(t *testing.T) {
	cfg := Config{
		BinaryPath: "x", WorkDir: "y", LogFile: "l", LogMaxSize: 1, EnvPATH: "p",
		EnvExtra: map[string]string{"EMPTY": "", "OK": "ok"},
	}
	script := buildWindowsTaskScript(cfg)
	if strings.Contains(script, "$env:EMPTY") {
		t.Errorf("empty value should be skipped: %s", script)
	}
}

func TestSchtasksInstall_ReplacesExistingScript(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	runPowerShell = func(script string) (string, error) { return "", nil }

	if err := os.MkdirAll(DefaultDataDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	scriptPath := windowsTaskScriptPath()
	if err := os.WriteFile(scriptPath, []byte("$env:OLD = 'leftover'\r\n"), 0o644); err != nil {
		t.Fatalf("seed legacy script: %v", err)
	}

	mgr := &schtasksManager{}
	cfg := Config{
		BinaryPath: "C:\\cc.exe",
		WorkDir:    t.TempDir(),
		LogFile:    "C:\\cc.log",
		LogMaxSize: 1024,
		EnvPATH:    "C:\\bin",
		EnvExtra:   map[string]string{"CUSTOM_TOKEN": "captured"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read installed script: %v", err)
	}
	script := string(data)
	if strings.Contains(script, "$env:OLD") {
		t.Fatalf("legacy script content survived reinstall:\n%s", script)
	}
	if !strings.Contains(script, "$env:CUSTOM_TOKEN = 'captured'") {
		t.Fatalf("installed script missing captured environment:\n%s", script)
	}
}
