package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var cachedLatestVersion struct {
	version   string
	timestamp time.Time
	checking  bool
	mu        sync.RWMutex
}

const versionCheckTTL = time.Hour

type updateCheckFunc func(string, bool) (*core.ReleaseInfo, error)

func fetchUpdateAsync(current string, check updateCheckFunc) {
	cachedLatestVersion.mu.Lock()
	if cachedLatestVersion.checking {
		cachedLatestVersion.mu.Unlock()
		return
	}
	cachedLatestVersion.checking = true
	cachedLatestVersion.mu.Unlock()
	go func() {
		release, err := check(current, false)
		cachedLatestVersion.mu.Lock()
		defer cachedLatestVersion.mu.Unlock()
		cachedLatestVersion.checking = false
		if err != nil {
			return
		}
		cachedLatestVersion.version = ""
		if release != nil {
			cachedLatestVersion.version = release.TagName
		}
		cachedLatestVersion.timestamp = time.Now()
	}()
}

func checkUpdateAsync() {
	if version == "" || strings.HasPrefix(version, "dev") {
		return
	}
	fetchUpdateAsync(version, core.CheckForUpdate)
}

func getUpdateHintIfAvailable() string { return cachedUpdateHint(version, core.CheckForUpdate) }

func cachedUpdateHint(current string, check updateCheckFunc) string {
	if current == "" || strings.HasPrefix(current, "dev") {
		return ""
	}
	cachedLatestVersion.mu.RLock()
	cachedVer, cachedTime := cachedLatestVersion.version, cachedLatestVersion.timestamp
	cachedLatestVersion.mu.RUnlock()
	if cachedTime.IsZero() || time.Since(cachedTime) > versionCheckTTL {
		fetchUpdateAsync(current, check)
		return ""
	}
	// Availability was decided by the shared checker, including fork timestamps.
	if cachedVer != "" && strings.TrimPrefix(cachedVer, "fork-") != strings.TrimPrefix(current, "fork-") {
		return fmt.Sprintf("\nUpdate available: %s -> %s (run: cc-connect update)\n", current, cachedVer)
	}
	return ""
}

func runUpdate(args []string) {
	if err := runUpdateCommand(args, version, os.Stdout, core.CheckForUpdate, core.SelfUpdate); err != nil {
		fmt.Fprintln(os.Stderr, "Update failed:", err)
		os.Exit(1)
	}
}

func runUpdateCommand(args []string, current string, out io.Writer, check updateCheckFunc, install func(string) error) error {
	pre, checkOnly := false, false
	for _, arg := range args {
		switch arg {
		case "--pre", "--beta":
			pre = true
		case "--check":
			checkOnly = true
		default:
			return fmt.Errorf("unknown update option: %s", arg)
		}
	}
	if _, err := fmt.Fprintf(out, "cc-connect %s\nUpdate repository: %s\nChecking for updates...\n", current, core.UpdateRepository); err != nil {
		return err
	}
	release, err := check(current, pre)
	if err != nil {
		return err
	}
	if release == nil {
		_, err := fmt.Fprintf(out, "Already up to date (%s).\n", current)
		return err
	}
	if _, err := fmt.Fprintf(out, "New version available: %s -> %s\n", current, release.TagName); err != nil {
		return err
	}
	if checkOnly {
		return nil
	}
	if err := install(release.TagName); err != nil {
		return err
	}
	if strings.EqualFold(core.UpdateRepository, "chenhg5/cc-connect") {
		if path, err := os.Executable(); err == nil {
			syncNpmPackageVersion(path, strings.TrimPrefix(release.TagName, "v"))
		}
	}
	if _, err := fmt.Fprintf(out, "Updated to %s\nRestart cc-connect to use the new version.\n", release.TagName); err != nil {
		return fmt.Errorf("update installed, but could not write receipt: %w", err)
	}
	return nil
}

func checkUpdate(args []string) {
	if err := runUpdateCommand(append([]string{"--check"}, args...), version, os.Stderr, core.CheckForUpdate, core.SelfUpdate); err != nil {
		fmt.Fprintln(os.Stderr, "Update check failed:", err)
	}
}

func isNewer(latest, current string) bool {
	if latest == "" || current == "" {
		return false
	}
	return strings.HasPrefix(current, "dev") || core.CompareUpdateVersions(latest, current) > 0
}

// Official npm installs keep package.json in sync so the wrapper does not
// download the previous version again. Fork updates refuse npm package targets.
func syncNpmPackageVersion(execPath, newVer string) {
	binDir := filepath.Dir(execPath)
	if filepath.Base(binDir) != "bin" {
		return
	}
	pkgJSON := filepath.Join(filepath.Dir(binDir), "package.json")
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		return
	}
	var pkg map[string]any
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	if name, _ := pkg["name"].(string); name != "cc-connect" {
		return
	}
	oldVer, _ := pkg["version"].(string)
	if strings.TrimPrefix(oldVer, "v") == strings.TrimPrefix(newVer, "v") {
		return
	}
	pkg["version"] = newVer
	out, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(pkgJSON, append(out, '\n'), 0o644); err != nil {
		slog.Warn("update: failed to sync npm package.json version", "error", err)
	}
}
