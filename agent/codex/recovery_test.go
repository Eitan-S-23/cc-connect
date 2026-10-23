package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const recoveryParentID = "11111111-1111-4111-8111-111111111111"
const recoveryChildID = "22222222-2222-4222-8222-222222222222"

func writeRecoveryFixture(t *testing.T, home, id string, records ...string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "23")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-23T07-00-00-"+id+".jsonl")
	body := []byte(strings.Join(records, "\n") + "\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func recoveryMessage(role, text string) string {
	data, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{
		"type": "message", "role": role, "content": []map[string]string{{"type": "input_text", "text": text}},
	}})
	return string(data)
}

func TestRecoveryExportsReadableContextWithoutChangingEncryptedHistory(t *testing.T) {
	home := t.TempDir()
	path, original := writeRecoveryFixture(t, home, recoveryParentID,
		`{"type":"session_meta","payload":{"history_mode":"paginated"}}`,
		recoveryMessage("developer", "OLD_SYSTEM_INSTRUCTIONS"),
		recoveryMessage("user", "# AGENTS.md instructions\nOLD_PROJECT_INSTRUCTIONS"),
		recoveryMessage("user", "keep existing behavior"),
		`{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"SECRET_CIPHERTEXT","content":"opaque-shape"}}`,
		`{"type":"response_item","payload":{"type":"compaction","encrypted_content":"SECRET_COMPACTION"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c1","input":"historical patch"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"tests passed"}}`,
		recoveryMessage("assistant", "implementation completed"),
	)
	a := &Agent{codexHome: home}
	got, err := a.GetSessionRecoveryContext(context.Background(), recoveryParentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keep existing behavior", "historical patch", "tests passed", "implementation completed"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"SECRET_CIPHERTEXT", "SECRET_COMPACTION", "OLD_SYSTEM_INSTRUCTIONS", "OLD_PROJECT_INSTRUCTIONS", "encrypted_content"} {
		if strings.Contains(got.Text, forbidden) {
			t.Errorf("exported %q", forbidden)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(original) != sha256.Sum256(after) {
		t.Fatal("original ciphertext or transcript was modified")
	}
	if got.Truncated {
		t.Fatal("small complete transcript was marked truncated")
	}
}

func TestRecoveryFollowsPaginatedForkPrefixNotLaterParentTurns(t *testing.T) {
	for _, cutoff := range []string{"both", "ordinal", "bytes"} {
		t.Run(cutoff, func(t *testing.T) {
			home := t.TempDir()
			meta := `{"type":"session_meta","payload":{}}`
			first := recoveryMessage("user", "inherited requirement")
			prefixBytes := len(meta) + len(first) + 2
			parentPath, parentBefore := writeRecoveryFixture(t, home, recoveryParentID, meta, first,
				recoveryMessage("user", "LATER_PARENT_REQUEST_MUST_NOT_LEAK"))
			base := map[string]any{"thread_id": recoveryParentID}
			if cutoff != "bytes" {
				base["end_ordinal_exclusive"] = 2
			}
			if cutoff != "ordinal" {
				base["end_byte_offset"] = prefixBytes
			}
			payload, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{
				"history_mode": "paginated", "history_base": base}})
			childPath, childBefore := writeRecoveryFixture(t, home, recoveryChildID, string(payload),
				recoveryMessage("assistant", "child-specific progress"))
			a := &Agent{codexHome: home}
			got, err := a.GetSessionRecoveryContext(context.Background(), recoveryChildID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Text, "inherited requirement") || !strings.Contains(got.Text, "child-specific progress") || strings.Contains(got.Text, "LATER_PARENT") {
				t.Fatalf("incorrect fork prefix: %s", got.Text)
			}
			for path, before := range map[string][]byte{parentPath: parentBefore, childPath: childBefore} {
				after, err := os.ReadFile(path)
				if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
					t.Fatal("fork recovery changed original history")
				}
			}
		})
	}
}

func TestRecoveryRejectsBrokenReferencesAndMalformedHistory(t *testing.T) {
	for _, tc := range []struct{ name, record string }{
		{"cycle", `{"type":"session_meta","payload":{"history_mode":"paginated","history_base":{"thread_id":"` + recoveryChildID + `","end_ordinal_exclusive":1}}}`},
		{"missing parent", `{"type":"session_meta","payload":{"history_mode":"paginated","history_base":{"thread_id":"` + recoveryParentID + `","end_ordinal_exclusive":2}}}`},
		{"missing cutoff", `{"type":"session_meta","payload":{"history_mode":"paginated","history_base":{"thread_id":"` + recoveryParentID + `"}}}`},
		{"malformed", "not json"},
		{"oversized line", strings.Repeat("x", maxRecoveryLineBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeRecoveryFixture(t, home, recoveryChildID, tc.record)
			a := &Agent{codexHome: home}
			if _, err := a.GetSessionRecoveryContext(context.Background(), recoveryChildID); err == nil {
				t.Fatal("expected a safe failure")
			}
		})
	}
}

func TestRecoveryBoundsUTF8ContextAndRetainsFirstRequest(t *testing.T) {
	b := &recoveryBuffer{}
	b.add("user", "ORIGINAL_REQUIREMENT")
	for i := 0; i < 30; i++ {
		b.add("tool result", strings.Repeat("\u4e2d", maxRecoveryEntryBytes))
	}
	b.add("assistant", "LATEST_PROGRESS")
	text := b.text()
	if len(text) > maxRecoveryBytes || !utf8.ValidString(text) || !b.truncated ||
		!strings.Contains(text, "ORIGINAL_REQUIREMENT") || !strings.Contains(text, "LATEST_PROGRESS") {
		t.Fatal("bounded recovery lost priority context or corrupted UTF-8")
	}
}

func TestRecoveryEmptyCompactionDoesNotInventReadableContext(t *testing.T) {
	b := &recoveryBuffer{truncated: true}
	if b.text() != "" {
		t.Fatal("a truncation marker alone is not recoverable context")
	}
}

func TestRecoveryDetectsSpecificErrorAndCancellation(t *testing.T) {
	a := &Agent{codexHome: t.TempDir()}
	if a.CanRecoverSession(nil) || a.CanRecoverSession(errors.New("400 invalid_request_error")) ||
		!a.CanRecoverSession(errors.New("400 invalid_encrypted_content")) {
		t.Fatal("incorrect error classification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.GetSessionRecoveryContext(ctx, recoveryParentID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

// Explicit opt-in, read-only validation against local records. No model,
// credentials or writable CODEX_HOME are used and no transcript text is logged.
func TestRecoveryReadOnlyLocalTranscript(t *testing.T) {
	home := os.Getenv("RECOVERY_READ_ONLY_SOURCE_HOME")
	ids := os.Getenv("RECOVERY_READ_ONLY_SOURCE_IDS")
	if home == "" || ids == "" {
		t.Skip("read-only local transcript validation is opt-in")
	}
	a := &Agent{codexHome: home}
	for _, id := range strings.Split(ids, ",") {
		path := findSessionFile(id, home)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.GetSessionRecoveryContext(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("source history changed")
		}
		if len(got.Text) == 0 || len(got.Text) > maxRecoveryBytes {
			t.Fatal("invalid recovery size")
		}
		t.Logf("read-only export: bytes=%d truncated=%v original_sha256_unchanged=true", len(got.Text), got.Truncated)
	}
}
