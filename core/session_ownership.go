package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const sessionOwnershipRegistryVersion = 1

var sessionOwnershipProcessMu sync.Mutex

// AgentSessionOwner identifies the cc-connect conversation allowed to resume
// an agent-side session. InternalSessionID is diagnostic only: /switch may map
// the same chat owner to another internal Session while retaining ownership.
type AgentSessionOwner struct {
	Project           string    `json:"project"`
	StorePath         string    `json:"store_path"`
	SessionKey        string    `json:"session_key"`
	InternalSessionID string    `json:"internal_session_id,omitempty"`
	ClaimedAt         time.Time `json:"claimed_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type sessionOwnershipSnapshot struct {
	Version int                                     `json:"version"`
	Owners  map[string]map[string]AgentSessionOwner `json:"owners"`
}

// SessionOwnershipConflictError reports that another cc-connect conversation
// already owns a single-writer agent session.
type SessionOwnershipConflictError struct {
	Scope     string
	ThreadID  string
	Existing  AgentSessionOwner
	Requested AgentSessionOwner
}

func (e *SessionOwnershipConflictError) Error() string {
	return fmt.Sprintf(
		"agent session %q in scope %q is owned by project %q session %q (store %q)",
		e.ThreadID,
		e.Scope,
		e.Existing.Project,
		e.Existing.SessionKey,
		e.Existing.StorePath,
	)
}

func IsSessionOwnershipConflict(err error) bool {
	var conflict *SessionOwnershipConflictError
	return errors.As(err, &conflict)
}

// SessionOwnershipRegistry persists ownership across cc-connect processes and
// serializes claims with an OS-level file lock.
type SessionOwnershipRegistry struct {
	path     string
	lockPath string
}

func NewSessionOwnershipRegistry(path string) *SessionOwnershipRegistry {
	path = canonicalOwnershipPath(path)
	return &SessionOwnershipRegistry{
		path:     path,
		lockPath: path + ".lock",
	}
}

func (r *SessionOwnershipRegistry) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Claim records owner as the persistent owner of (scope, threadID). Repeated
// claims by the same project/store/session tuple are idempotent.
func (r *SessionOwnershipRegistry) Claim(scope, threadID string, owner AgentSessionOwner) error {
	if r == nil || r.path == "" || strings.TrimSpace(threadID) == "" || threadID == ContinueSession {
		return nil
	}

	scope = canonicalOwnershipPath(scope)
	if scope == "" {
		return fmt.Errorf("session ownership scope is empty")
	}
	threadID = strings.TrimSpace(threadID)
	owner = canonicalAgentSessionOwner(owner)
	if owner.Project == "" || owner.SessionKey == "" {
		return fmt.Errorf("session ownership owner is incomplete")
	}

	return r.withLockedSnapshot(func(snap *sessionOwnershipSnapshot) (bool, error) {
		byThread := snap.Owners[scope]
		if byThread == nil {
			byThread = make(map[string]AgentSessionOwner)
			snap.Owners[scope] = byThread
		}

		now := time.Now().UTC()
		if existing, ok := byThread[threadID]; ok {
			if !sameAgentSessionOwner(existing, owner) {
				return false, &SessionOwnershipConflictError{
					Scope:     scope,
					ThreadID:  threadID,
					Existing:  existing,
					Requested: owner,
				}
			}
			owner.ClaimedAt = existing.ClaimedAt
			if owner.ClaimedAt.IsZero() {
				owner.ClaimedAt = now
			}
			owner.UpdatedAt = now
			if existing.InternalSessionID == owner.InternalSessionID && existing.Project == owner.Project &&
				existing.StorePath == owner.StorePath && existing.SessionKey == owner.SessionKey {
				return false, nil
			}
			byThread[threadID] = owner
			return true, nil
		}

		owner.ClaimedAt = now
		owner.UpdatedAt = now
		byThread[threadID] = owner
		return true, nil
	})
}

// Release removes a thread after an explicit agent-side delete. It does not
// run during process shutdown or ordinary session rotation.
func (r *SessionOwnershipRegistry) Release(scope, threadID string) error {
	if r == nil || r.path == "" || strings.TrimSpace(threadID) == "" {
		return nil
	}
	scope = canonicalOwnershipPath(scope)
	threadID = strings.TrimSpace(threadID)
	return r.withLockedSnapshot(func(snap *sessionOwnershipSnapshot) (bool, error) {
		byThread := snap.Owners[scope]
		if byThread == nil {
			return false, nil
		}
		if _, ok := byThread[threadID]; !ok {
			return false, nil
		}
		delete(byThread, threadID)
		if len(byThread) == 0 {
			delete(snap.Owners, scope)
		}
		return true, nil
	})
}

func (r *SessionOwnershipRegistry) withLockedSnapshot(fn func(*sessionOwnershipSnapshot) (bool, error)) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create session ownership directory: %w", err)
	}

	// OS file locks are the cross-process guard. This mutex also serializes
	// separate registry instances inside one process on every supported OS.
	sessionOwnershipProcessMu.Lock()
	defer sessionOwnershipProcessMu.Unlock()

	unlock, err := acquireSessionOwnershipFileLock(r.lockPath)
	if err != nil {
		return fmt.Errorf("lock session ownership registry: %w", err)
	}
	defer unlock()

	snap, err := r.load()
	if err != nil {
		return err
	}
	changed, err := fn(snap)
	if err != nil || !changed {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session ownership registry: %w", err)
	}
	if err := AtomicWriteFile(r.path, data, 0o600); err != nil {
		return fmt.Errorf("write session ownership registry: %w", err)
	}
	return nil
}

func (r *SessionOwnershipRegistry) load() (*sessionOwnershipSnapshot, error) {
	snap := &sessionOwnershipSnapshot{
		Version: sessionOwnershipRegistryVersion,
		Owners:  make(map[string]map[string]AgentSessionOwner),
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return nil, fmt.Errorf("read session ownership registry: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("session ownership registry is empty: %s", r.path)
	}
	if err := json.Unmarshal(data, snap); err != nil {
		return nil, fmt.Errorf("parse session ownership registry: %w", err)
	}
	if snap.Version != sessionOwnershipRegistryVersion {
		return nil, fmt.Errorf("unsupported session ownership registry version %d", snap.Version)
	}
	if snap.Owners == nil {
		snap.Owners = make(map[string]map[string]AgentSessionOwner)
	}
	return snap, nil
}

func sameAgentSessionOwner(a, b AgentSessionOwner) bool {
	return a.Project == b.Project && a.StorePath == b.StorePath && a.SessionKey == b.SessionKey
}

func canonicalAgentSessionOwner(owner AgentSessionOwner) AgentSessionOwner {
	owner.Project = strings.TrimSpace(owner.Project)
	owner.StorePath = canonicalOwnershipPath(owner.StorePath)
	owner.SessionKey = strings.TrimSpace(owner.SessionKey)
	owner.InternalSessionID = strings.TrimSpace(owner.InternalSessionID)
	return owner
}

func canonicalOwnershipPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
