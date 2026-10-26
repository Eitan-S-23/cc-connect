package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type projectStateData struct {
	AutoRetryEnabled        bool              `json:"auto_retry_enabled,omitempty"`
	NotifyEnabled           *bool             `json:"notify_enabled,omitempty"`
	WorkDirOverride         string            `json:"work_dir_override,omitempty"`
	WorkspaceDirOverrides   map[string]string `json:"workspace_dir_overrides,omitempty"`
	WorkspaceModelOverrides map[string]string `json:"workspace_model_overrides,omitempty"`
}

// ProjectStateStore persists lightweight runtime state for one project.
type ProjectStateStore struct {
	mu        sync.RWMutex
	storePath string
	state     projectStateData
}

func NewProjectStateStore(path string) *ProjectStateStore {
	ps := &ProjectStateStore{storePath: path}
	if path != "" {
		ps.load()
	}
	return ps
}

func (ps *ProjectStateStore) NotifyEnabled() (enabled, overridden bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.state.NotifyEnabled == nil {
		return false, false
	}
	return *ps.state.NotifyEnabled, true
}

func (ps *ProjectStateStore) AutoRetryEnabled() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.state.AutoRetryEnabled
}

func (ps *ProjectStateStore) SetAutoRetryEnabled(enabled bool) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	previous := ps.state.AutoRetryEnabled
	ps.state.AutoRetryEnabled = enabled
	if err := ps.saveLocked(); err != nil {
		ps.state.AutoRetryEnabled = previous
		return err
	}
	return nil
}

// SetNotifyEnabled publishes the override only after it has been saved.
func (ps *ProjectStateStore) SetNotifyEnabled(enabled bool) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	previous := ps.state.NotifyEnabled
	ps.state.NotifyEnabled = &enabled
	if err := ps.saveLocked(); err != nil {
		ps.state.NotifyEnabled = previous
		return err
	}
	return nil
}

func (ps *ProjectStateStore) WorkDirOverride() string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.state.WorkDirOverride
}

func (ps *ProjectStateStore) SetWorkDirOverride(dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.state.WorkDirOverride = dir
}

func (ps *ProjectStateStore) WorkspaceDirOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.state.WorkspaceDirOverrides == nil {
		return ""
	}
	return ps.state.WorkspaceDirOverrides[workspace]
}

func (ps *ProjectStateStore) SetWorkspaceDirOverride(workspace, dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		ps.state.WorkspaceDirOverrides = make(map[string]string)
	}
	ps.state.WorkspaceDirOverrides[workspace] = dir
}

func (ps *ProjectStateStore) ClearWorkspaceDirOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceDirOverrides, workspace)
	if len(ps.state.WorkspaceDirOverrides) == 0 {
		ps.state.WorkspaceDirOverrides = nil
	}
}

func (ps *ProjectStateStore) WorkspaceModelOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.state.WorkspaceModelOverrides == nil {
		return ""
	}
	return ps.state.WorkspaceModelOverrides[workspace]
}

func (ps *ProjectStateStore) SetWorkspaceModelOverride(workspace, model string) {
	if model == "" {
		ps.ClearWorkspaceModelOverride(workspace)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		ps.state.WorkspaceModelOverrides = make(map[string]string)
	}
	ps.state.WorkspaceModelOverrides[workspace] = model
}

func (ps *ProjectStateStore) ClearWorkspaceModelOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceModelOverrides, workspace)
	if len(ps.state.WorkspaceModelOverrides) == 0 {
		ps.state.WorkspaceModelOverrides = nil
	}
}

func (ps *ProjectStateStore) ClearWorkDirOverride() {
	ps.SetWorkDirOverride("")
}

func (ps *ProjectStateStore) Save() {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if err := ps.saveLocked(); err != nil {
		slog.Error("project_state: failed to save", "path", ps.storePath, "error", err)
	}
}

func (ps *ProjectStateStore) saveLocked() error {
	if ps.storePath == "" {
		return nil
	}

	data, err := json.MarshalIndent(ps.state, "", "  ")
	if err != nil {
		return fmt.Errorf("project_state: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(ps.storePath), 0o755); err != nil {
		return fmt.Errorf("project_state: create dir: %w", err)
	}
	if err := AtomicWriteFile(ps.storePath, data, 0o644); err != nil {
		return fmt.Errorf("project_state: write: %w", err)
	}
	return nil
}

func (ps *ProjectStateStore) load() {
	data, err := os.ReadFile(ps.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("project_state: failed to read", "path", ps.storePath, "error", err)
		}
		return
	}

	var state projectStateData
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("project_state: failed to unmarshal", "path", ps.storePath, "error", err)
		return
	}
	ps.state = state
}
