package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
)

const (
	maxRecoveryBytes      = 128 * 1024
	maxRecoveryEntryBytes = 16 * 1024
	maxRecoveryLineBytes  = 8 * 1024 * 1024
)

func (a *Agent) CanRecoverSession(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_encrypted_content")
}

func (a *Agent) GetSessionRecoveryContext(ctx context.Context, id string) (core.SessionRecoveryContext, error) {
	a.mu.RLock()
	home := a.codexHome
	a.mu.RUnlock()
	b := &recoveryBuffer{}
	if err := readRecoveryHistory(ctx, id, home, nil, nil, map[string]bool{}, b); err != nil {
		return core.SessionRecoveryContext{}, fmt.Errorf("codex recovery: %w", err)
	}
	return core.SessionRecoveryContext{Text: b.text(), Truncated: b.truncated}, nil
}

// Retain the initial request and a bounded tail, never opaque response items.
type recoveryBuffer struct {
	firstUser string
	recent    []string
	bytes     int
	truncated bool
}

func (b *recoveryBuffer) add(role, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > maxRecoveryEntryBytes {
		text = recoveryUTF8Prefix(text, maxRecoveryEntryBytes/2) + "\n[... truncated ...]\n" +
			recoveryUTF8Suffix(text, maxRecoveryEntryBytes/2-32)
		b.truncated = true
	}
	entry := fmt.Sprintf("[%s]\n%s\n\n", role, text)
	if role == "user" && b.firstUser == "" {
		b.firstUser = entry
	} else {
		b.recent = append(b.recent, entry)
		b.bytes += len(entry)
	}
	for b.bytes+len(b.firstUser) > maxRecoveryBytes-128 && len(b.recent) > 0 {
		b.bytes -= len(b.recent[0])
		b.recent = b.recent[1:]
		b.truncated = true
	}
}

func (b *recoveryBuffer) text() string {
	if b.firstUser == "" && len(b.recent) == 0 {
		return ""
	}
	marker := ""
	if b.truncated {
		marker = "[History compacted or truncated; original records remain unchanged.]\n\n"
	}
	return marker + b.firstUser + strings.Join(b.recent, "")
}

func recoveryUTF8Prefix(s string, n int) string {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func recoveryUTF8Suffix(s string, n int) string {
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

type recoveryRecord struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type recoveryBase struct {
	ThreadID string `json:"thread_id"`
	End      *int64 `json:"end_ordinal_exclusive"`
	Bytes    *int64 `json:"end_byte_offset"`
}

// Paginated forks reference a bounded parent prefix instead of copying it.
// Following the reference without its cutoff would import later parent turns.
func readRecoveryHistory(ctx context.Context, id, home string, end, byteEnd *int64, visiting map[string]bool, b *recoveryBuffer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid session id")
	}
	if visiting[id] || len(visiting) >= 32 {
		return fmt.Errorf("cyclic or excessively deep history reference")
	}
	if (end != nil && *end < 0) || (byteEnd != nil && *byteEnd < 0) {
		return fmt.Errorf("invalid history cutoff")
	}
	visiting[id] = true
	defer delete(visiting, id)
	path := findSessionFile(id, home)
	if path == "" {
		return fmt.Errorf("session history not found for %s", id)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var reader io.Reader = f
	if byteEnd != nil {
		stat, err := f.Stat()
		if err != nil {
			return err
		}
		if *byteEnd > stat.Size() {
			return fmt.Errorf("parent history is shorter than its saved cutoff")
		}
		reader = io.LimitReader(f, *byteEnd)
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxRecoveryLineBytes)
	var ordinal int64
	for (end == nil || ordinal < *end) && scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		ordinal++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var record recoveryRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("invalid history record at ordinal %d", ordinal-1)
		}
		if record.Type == "session_meta" {
			if err := readRecoveryBase(ctx, record.Payload, home, visiting, b); err != nil {
				return err
			}
		} else if record.Type == "response_item" {
			if err := addRecoveryItem(record.Payload, b); err != nil {
				return err
			}
		} else if record.Type == "compacted" {
			var compact struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(record.Payload, &compact); err != nil {
				return fmt.Errorf("invalid compaction record")
			}
			b.add("summary", compact.Message)
			b.truncated = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read history: %w", err)
	}
	if end != nil && ordinal < *end {
		return fmt.Errorf("parent history is shorter than its saved ordinal")
	}
	return nil
}

func readRecoveryBase(ctx context.Context, payload json.RawMessage, home string, visiting map[string]bool, b *recoveryBuffer) error {
	var meta struct {
		HistoryMode string        `json:"history_mode"`
		HistoryBase *recoveryBase `json:"history_base"`
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return fmt.Errorf("invalid session metadata")
	}
	if meta.HistoryBase == nil {
		return nil
	}
	base := meta.HistoryBase
	if meta.HistoryMode != "paginated" || base.ThreadID == "" || (base.End == nil && base.Bytes == nil) {
		return fmt.Errorf("unsupported or incomplete history reference")
	}
	return readRecoveryHistory(ctx, base.ThreadID, home, base.End, base.Bytes, visiting, b)
}

func addRecoveryItem(payload json.RawMessage, b *recoveryBuffer) error {
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Name    string          `json:"name"`
		CallID  string          `json:"call_id"`
		Input   string          `json:"input"`
		Args    string          `json:"arguments"`
		Output  json.RawMessage `json:"output"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	// Inspect the type first: encrypted and other opaque items may contain
	// incompatible shapes and must not be interpreted as readable messages.
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &kind); err != nil {
		return fmt.Errorf("invalid response item")
	}
	switch kind.Type {
	case "message", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
	default:
		return nil
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return fmt.Errorf("invalid readable response item")
	}
	switch item.Type {
	case "message":
		if item.Role != "user" && item.Role != "assistant" {
			return nil
		}
		for _, c := range item.Content {
			if c.Type != "input_text" && c.Type != "output_text" {
				continue
			}
			text := strings.TrimSpace(c.Text)
			if item.Role == "user" && (strings.HasPrefix(text, "# AGENTS.md") ||
				strings.HasPrefix(text, "#AGENTS.md") || strings.HasPrefix(text, "<environment_context>")) {
				continue
			}
			b.add(item.Role, c.Text)
		}
	case "function_call", "custom_tool_call":
		b.add("tool call (historical)", item.Name+" "+item.CallID+"\n"+item.Args+item.Input)
	case "function_call_output", "custom_tool_call_output":
		var output string
		if json.Unmarshal(item.Output, &output) != nil {
			output = string(item.Output)
		}
		b.add("tool result (historical)", item.CallID+"\n"+output)
	}
	return nil
}
