package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UpdateTransaction identifies exactly the files owned by one installation.
// A verified download is installed before shutdown; activation is a separate step.
type UpdateTransaction struct {
	ID             string    `json:"id"`
	Target         string    `json:"target"`
	Version        string    `json:"version"`
	PreviousSHA256 string    `json:"previous_sha256"`
	SHA256         string    `json:"sha256"`
	State          string    `json:"state"`
	Created        time.Time `json:"created"`
}

const updateHistoryLimit = 12

func executableDigest(path string) (string, error) {
	if err := CheckUpdatePath(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ExecutableSHA256 hashes a regular executable without following a reparse point.
func ExecutableSHA256(path string) (string, error) { return executableDigest(path) }

// CheckUpdatePath rejects links/reparse points in every existing path component.
// Call it before creating outputs as well as before opening existing artifacts.
func CheckUpdatePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := abs; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && updatePathIsLink(info) {
			return fmt.Errorf("update path contains a link or reparse point: %s", current)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func (t UpdateTransaction) Directory() string { return filepath.Join(t.Target+".updates", t.ID) }
func (t UpdateTransaction) Backup() string    { return filepath.Join(t.Directory(), "previous.exe") }

func (t UpdateTransaction) save() error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(t.Directory(), "receipt.json")
	if err := CheckUpdatePath(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(t.Directory(), ".receipt-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func updateTransactions(target string) ([]UpdateTransaction, error) {
	root := target + ".updates"
	if err := CheckUpdatePath(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var transactions []UpdateTransaction
	for _, entry := range entries {
		id := entry.Name()
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || !entry.IsDir() {
			return nil, fmt.Errorf("unrecognized update history entry: %s", id)
		}
		path := filepath.Join(root, id, "receipt.json")
		if err := CheckUpdatePath(path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var tx UpdateTransaction
		if err := json.Unmarshal(data, &tx); err != nil {
			return nil, err
		}
		if tx.Target != target || tx.ID != id {
			return nil, fmt.Errorf("update receipt identity mismatch: %s", path)
		}
		transactions = append(transactions, tx)
	}
	sort.Slice(transactions, func(i, j int) bool { return transactions[i].Created.After(transactions[j].Created) })
	return transactions, nil
}

// PendingUpdate returns the installation whose activation has not been confirmed.
func PendingUpdate(target string) (*UpdateTransaction, error) {
	all, err := updateTransactions(target)
	if err != nil {
		return nil, err
	}
	var pending *UpdateTransaction
	for _, tx := range all {
		if tx.State != "installed" && tx.State != "prepared" {
			continue
		}
		if pending != nil {
			return nil, fmt.Errorf("multiple pending update transactions")
		}
		copy := tx
		pending = &copy
	}
	return pending, nil
}

// InstallUpdateTransaction installs bytes already verified against the release
// archive and checksums. The updater is the production caller; isolated Windows
// regressions use it with their own executables, never a live installation.
func InstallUpdateTransaction(target string, data []byte, version string) (*UpdateTransaction, error) {
	return installUpdateTransaction(target, data, version, os.Rename)
}

func installUpdateTransaction(target string, data []byte, version string, rename func(string, string) error) (*UpdateTransaction, error) {
	target, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	if err := CheckUpdatePath(target); err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("update target is not a regular file")
	}
	lock, err := os.OpenFile(target+".update-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire update lock: %w", err)
	}
	defer func() { _ = lock.Close(); removeUpdateFile(target + ".update-lock") }()
	if pending, err := PendingUpdate(target); err != nil {
		return nil, err
	} else if pending != nil {
		return nil, fmt.Errorf("update %s has not been activated; complete its restart or recovery first", pending.ID)
	}
	if err := pruneUpdateHistory(target); err != nil {
		return nil, err
	}
	oldSHA, err := executableDigest(target)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	tx := &UpdateTransaction{ID: hex.EncodeToString(nonce[:]), Target: target, Version: version, PreviousSHA256: oldSHA, SHA256: hex.EncodeToString(sum[:]), State: "prepared", Created: time.Now().UTC()}
	if err := CheckUpdatePath(tx.Directory()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(target+".updates", 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(tx.Directory(), 0o700); err != nil {
		return nil, err
	}
	if err := tx.save(); err != nil {
		return nil, err
	}
	staged := filepath.Join(tx.Directory(), "new.exe")
	defer removeUpdateFile(staged)
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0o111)
	if err != nil {
		return nil, recordInstallFailure(tx, err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		err = writeErr
	} else if syncErr != nil {
		err = syncErr
	} else {
		err = closeErr
	}
	if err != nil {
		return nil, recordInstallFailure(tx, err)
	}
	if err := rename(target, tx.Backup()); err != nil {
		return nil, recordInstallFailure(tx, fmt.Errorf("backup old executable: %w", err))
	}
	if err := rename(staged, target); err != nil {
		if restoreErr := rename(tx.Backup(), target); restoreErr != nil {
			return nil, fmt.Errorf("install failed: %v; rollback failed (backup at %s): %w", err, tx.Backup(), restoreErr)
		}
		return nil, recordInstallFailure(tx, fmt.Errorf("install failed; original executable restored: %w", err))
	}
	tx.State = "installed"
	if err := tx.save(); err != nil {
		return nil, fmt.Errorf("installed executable but could not persist receipt: %w", err)
	}
	return tx, nil
}

func recordInstallFailure(tx *UpdateTransaction, err error) error {
	tx.State = "install_failed"
	return errors.Join(err, tx.save())
}

// ActivateUpdate requires the digest captured by the running new instance.
func ActivateUpdate(target, runningSHA string) error {
	tx, err := PendingUpdate(target)
	if err != nil || tx == nil {
		return err
	}
	if tx.State != "installed" || tx.SHA256 != runningSHA {
		return fmt.Errorf("running executable does not match pending update")
	}
	tx.State = "healthy"
	return tx.save()
}

// RollbackUpdate is called only after the coordinator has stopped and waited
// for the new instance. Preserve the failed image for diagnosis and never delete
// or overwrite the only usable previous executable.
func RollbackUpdate(target string) error {
	tx, err := PendingUpdate(target)
	if err != nil {
		return err
	}
	if tx == nil || tx.State != "installed" {
		return fmt.Errorf("no installed transaction to roll back")
	}
	old, err := executableDigest(tx.Backup())
	if err != nil {
		return err
	}
	current, err := executableDigest(target)
	if err != nil {
		return err
	}
	if old != tx.PreviousSHA256 || current != tx.SHA256 {
		return fmt.Errorf("rollback executable checksum mismatch")
	}
	failed := filepath.Join(tx.Directory(), "failed.exe")
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		return fmt.Errorf("rollback destination already exists or cannot be inspected: %v", err)
	}
	if err := os.Rename(target, failed); err != nil {
		return fmt.Errorf("preserve failed executable: %w", err)
	}
	if err := os.Rename(tx.Backup(), target); err != nil {
		if restoreErr := os.Rename(failed, target); restoreErr != nil {
			return fmt.Errorf("rollback failed: %v; restoring new executable also failed: %w", err, restoreErr)
		}
		return fmt.Errorf("rollback failed; new executable retained: %w", err)
	}
	tx.State = "rolled_back"
	return tx.save()
}

func pruneUpdateHistory(target string) error {
	all, err := updateTransactions(target)
	if err != nil {
		return err
	}
	retained := 0
	for i, tx := range all {
		if i < 2 {
			retained++
			continue
		}
		if tx.State != "healthy" && tx.State != "rolled_back" && tx.State != "install_failed" {
			return fmt.Errorf("cannot clean unresolved update %s", tx.ID)
		}
		entries, err := os.ReadDir(tx.Directory())
		if err != nil {
			return err
		}
		busy := false
		for _, entry := range entries {
			if entry.Name() != "receipt.json" && entry.Name() != "previous.exe" && entry.Name() != "failed.exe" {
				return fmt.Errorf("unrecognized file in update transaction: %s", entry.Name())
			}
			path := filepath.Join(tx.Directory(), entry.Name())
			if err := CheckUpdatePath(path); err != nil {
				return err
			}
			if entry.IsDir() {
				return fmt.Errorf("unexpected directory in update transaction")
			}
			if strings.HasSuffix(entry.Name(), ".exe") {
				digest, err := executableDigest(path)
				if err != nil {
					return err
				}
				want := tx.PreviousSHA256
				if entry.Name() == "failed.exe" {
					want = tx.SHA256
				}
				if digest != want {
					return fmt.Errorf("historical executable no longer matches its receipt: %s", path)
				}
				inUse, err := updateFileInUse(path)
				if err != nil {
					return fmt.Errorf("inspect historical backup: %w", err)
				}
				busy = busy || inUse
			}
		}
		if busy {
			retained++
			continue
		}
		// No wildcard deletion: only validated receipt-owned files are removed.
		for _, entry := range entries {
			if entry.Name() == "receipt.json" {
				continue
			}
			if err := os.Remove(filepath.Join(tx.Directory(), entry.Name())); err != nil {
				return fmt.Errorf("remove historical backup: %w", err)
			}
		}
		if err := os.Remove(filepath.Join(tx.Directory(), "receipt.json")); err != nil {
			return err
		}
		if err := os.Remove(tx.Directory()); err != nil {
			return err
		}
	}
	if retained >= updateHistoryLimit {
		return fmt.Errorf("too many retained update transactions (%d); release occupied backups before updating", retained)
	}
	return nil
}
