package codex

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAgentSessionOwnershipScope_UsesExplicitCodexHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex-home")
	agent := &Agent{codexHome: home}

	got, err := agent.SessionOwnershipScope()
	if err != nil {
		t.Fatalf("SessionOwnershipScope() error = %v", err)
	}
	want, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("SessionOwnershipScope() = %q, want %q", got, filepath.Clean(want))
	}
}

func TestAgentSessionOwnershipScope_ResolvesRelativeHomeFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	agent := &Agent{codexHome: ".codex-local", workDir: workDir}

	got, err := agent.SessionOwnershipScope()
	if err != nil {
		t.Fatalf("SessionOwnershipScope() error = %v", err)
	}
	want := filepath.Join(workDir, ".codex-local")
	if got != filepath.Clean(want) {
		t.Fatalf("SessionOwnershipScope() = %q, want %q", got, filepath.Clean(want))
	}
}

func TestAgentIsSessionResumeConflict_RecognizesSingleWriterErrors(t *testing.T) {
	agent := &Agent{}
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("thread-store conflict: thread abc already has an active writer"), true},
		{errors.New("thread/resume failed: already has an active writer"), true},
		{errors.New("session not found"), false},
		{nil, false},
	}
	for _, tc := range tests {
		if got := agent.IsSessionResumeConflict(tc.err); got != tc.want {
			t.Errorf("IsSessionResumeConflict(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
