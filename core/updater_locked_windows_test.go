//go:build windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This test also runs unchanged on the pre-fix source in Actions. A real mapped
// executable keeps .old occupied; mocking os.Remove cannot reproduce this bug.
func TestWindowsUpgradeWithMappedLegacyBackup(t *testing.T) {
	if os.Getenv("CC_UPDATE_LOCK_HELPER") == "1" {
		_ = os.WriteFile(os.Getenv("CC_UPDATE_LOCK_READY"), []byte("ready"), 0o600)
		for {
			time.Sleep(time.Hour)
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "bot.exe")
	ready := filepath.Join(dir, "ready")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, image, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(target, "-test.run=^TestWindowsUpgradeWithMappedLegacyBackup$")
	helper.Env = append(os.Environ(), "CC_UPDATE_LOCK_HELPER=1", "CC_UPDATE_LOCK_READY="+ready)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not map executable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.Rename(target, target+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, image, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target + ".old"); err == nil {
		t.Fatal("fixture did not retain a mapped legacy backup")
	}
	if err := replaceBinaryAt(target, image, os.Rename); err != nil {
		t.Fatalf("REGRESSION: mapped historical backup prevents the next upgrade: %v", err)
	}
}
