package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateTransactionsRetainRollbackAndRejectOverlap(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cc-connect.exe")
	if err := os.WriteFile(target, []byte("A"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"B", "C", "D", "E"} {
		tx, err := InstallUpdateTransaction(target, []byte(version), version)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := InstallUpdateTransaction(target, []byte("overlap"), "overlap"); err == nil {
			t.Fatal("overlapping transaction accepted")
		}
		if err := ActivateUpdate(target, "wrong digest"); err == nil {
			t.Fatal("wrong image activated")
		}
		if err := ActivateUpdate(target, tx.SHA256); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(tx.Backup()); err != nil {
			t.Fatal("rollback copy lost", err)
		}
	}
	all, err := updateTransactions(target)
	if err != nil || len(all) > 3 {
		t.Fatalf("unbounded history: %d %v", len(all), err)
	}
	tx, err := InstallUpdateTransaction(target, []byte("broken"), "F")
	if err != nil {
		t.Fatal(err)
	}
	if err := RollbackUpdate(target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "E" {
		t.Fatalf("rollback = %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(tx.Directory(), "failed.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateTransactionInstallAndRollbackFailures(t *testing.T) {
	for _, failure := range []string{"backup", "install", "rollback"} {
		t.Run(failure, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "bot.exe")
			if err := os.WriteFile(target, []byte("original"), 0o700); err != nil {
				t.Fatal(err)
			}
			rename := func(src, dst string) error {
				if failure == "backup" && src == target {
					return os.ErrPermission
				}
				if filepath.Base(src) == "new.exe" {
					return errors.New("install refused")
				}
				if failure == "rollback" && filepath.Base(src) == "previous.exe" {
					return errors.New("restore refused")
				}
				return os.Rename(src, dst)
			}
			_, err := installUpdateTransaction(target, []byte("new"), "B", rename)
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if failure == "rollback" {
				if !strings.Contains(err.Error(), "rollback failed") {
					t.Fatal(err)
				}
				all, e := updateTransactions(target)
				if e != nil || len(all) != 1 {
					t.Fatal(all, e)
				}
				data, e := os.ReadFile(all[0].Backup())
				if e != nil || string(data) != "original" {
					t.Fatal("rollback copy lost", e)
				}
			} else {
				data, e := os.ReadFile(target)
				if e != nil || string(data) != "original" {
					t.Fatal("original lost", e)
				}
			}
			if failure == "backup" && !errors.Is(err, os.ErrPermission) {
				t.Fatal("permission cause lost", err)
			}
		})
	}
}

func TestUpdateTransactionRejectsUnownedHistory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "bot.exe")
	_ = os.WriteFile(target, []byte("A"), 0o700)
	unknown := filepath.Join(target+".updates", "someone-elses-file")
	if err := os.MkdirAll(filepath.Dir(unknown), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallUpdateTransaction(target, []byte("B"), "B"); err == nil {
		t.Fatal("unexpected history accepted")
	}
	if got, err := os.ReadFile(unknown); err != nil || string(got) != "keep" {
		t.Fatal("unowned file modified")
	}
}
