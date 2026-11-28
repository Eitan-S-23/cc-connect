//go:build windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsUpdateRetainsOccupiedTransactionAndLaterCleansIt(t *testing.T) {
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
			t.Fatal("mapped-image helper timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var first *UpdateTransaction
	for _, v := range []string{"B", "C", "D", "E"} {
		tx, err := InstallUpdateTransaction(target, image, v)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = tx
		}
		if err := ActivateUpdate(target, tx.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(first.Backup()); err != nil {
		t.Fatal("occupied backup was removed", err)
	}
	busy, err := updateFileInUse(first.Backup())
	if err != nil || !busy {
		t.Fatalf("mapped image not detected: %v %v", busy, err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	if _, err := InstallUpdateTransaction(target, image, "F"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Directory()); !os.IsNotExist(err) {
		t.Fatal("released historical transaction not cleaned", err)
	}
}

func TestWindowsUpdatePermissionErrorIsNotTreatedAsOccupiedBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bot.exe")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real deny DELETE ACL on the executable: administrator privilege does
	// not silently convert this error into the occupied-history exception.
	sd, err := windows.SecurityDescriptorFromString("D:(D;;SD;;;WD)(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	dirSD, err := windows.SecurityDescriptorFromString("D:(D;;0x40;;;WD)(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dirACL, _, err := dirSD.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dirACL, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		allow, _ := windows.SecurityDescriptorFromString("D:(A;;FA;;;WD)")
		acl, _, _ := allow.DACL()
		_ = windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
		_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	})
	_, err = InstallUpdateTransaction(target, []byte("replacement"), "B")
	if err == nil || !strings.Contains(err.Error(), "backup old executable") {
		t.Fatalf("real ACL failure was hidden: %v", err)
	}
	if data, e := os.ReadFile(target); e != nil || string(data) != "original" {
		t.Fatal("permission failure changed executable", e)
	}
}

func TestWindowsUpdateRejectsReparsePoint(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "junction")
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create isolated junction: %s %v", out, err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if err := CheckUpdatePath(filepath.Join(link, "new.exe")); err == nil {
		t.Fatal("reparse point accepted")
	}
}
