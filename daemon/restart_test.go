package daemon

import (
	"path/filepath"
	"testing"
)

func TestPreparedRestartRejectsExpiredController(t *testing.T) {
	dir := t.TempDir()
	prepared := &PreparedRestart{directory: dir, id: "transaction"}
	if err := writeRestartJSON(filepath.Join(dir, "result.json"), restartResult{ID: "transaction", Error: "shutdown deadline expired"}); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(); err == nil {
		t.Fatal("shutdown committed after controller expired")
	}
}
