package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// resolveCodexHomeDir returns the effective CODEX_HOME directory.
// Priority: explicit config value > CODEX_HOME env > ~/.codex
func resolveCodexHomeDir(explicit string) string {
	if h := strings.TrimSpace(explicit); h != "" {
		return h
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".codex")
}

// listCodexSessions scans the codex sessions directory for JSONL transcript
// files whose cwd matches workDir.
//
// 性能约定：归属过滤只读 rollout 首行；一次遍历同时建立 thread_id → 路径索引，
// 取代“每个命中会话都全目录重扫一次”的旧实现（原为 O(命中数 × 文件总数)）；
// 全程响应 ctx 取消，长扫描不会无限期占住会话。
func listCodexSessions(ctx context.Context, workDir, codexHome string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")
	files, pathByThread, err := scanCodexSessionFiles(ctx, sessionsDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	sessionTitles := loadCodexSessionTitles(codexHome)
	var sessions []core.AgentSessionInfo
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info := parseCodexSessionFile(f, absWorkDir)
		if info == nil {
			continue
		}
		if title := sessionTitles[info.ID]; title != "" {
			if titleRunes := []rune(title); len(titleRunes) > 60 {
				title = string(titleRunes[:60]) + "..."
			}
			info.Summary = title
		}
		if path := pathByThread[info.ID]; path != "" {
			patchSessionSourceFile(path)
		} else {
			// 文件名不含 thread_id 时的兜底，维持旧行为
			patchSessionSource(info.ID, codexHome)
		}
		sessions = append(sessions, *info)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// scanCodexSessionFiles 一次遍历收集全部 rollout 文件，并顺带建立
// thread_id → 文件路径 索引，供 patchSessionSource 直接使用。
func scanCodexSessionFiles(ctx context.Context, sessionsDir string) ([]string, map[string]string, error) {
	var files []string
	pathByThread := make(map[string]string)
	err := filepath.WalkDir(sessionsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if walkErr != nil {
			// 与旧实现一致：单个目录/文件不可读时跳过，不中断整体扫描
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		files = append(files, path)
		if id := sessionIDFromRolloutName(entry.Name()); id != "" {
			if _, exists := pathByThread[id]; !exists {
				pathByThread[id] = path // 与旧 findSessionFile 一致：首个命中优先
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, pathByThread, nil
}

// sessionIDFromRolloutName 从 rollout 文件名提取 thread_id。
// 形如 rollout-2026-09-18T23-43-01-<uuid>.jsonl：时间戳固定 19 字符，
// 其后紧跟 '-' 与 UUID。格式不符时返回空串，由 findSessionFile 兜底。
func sessionIDFromRolloutName(name string) string {
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return ""
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	const stampLen = len("2006-01-02T15-04-05")
	if len(stem) <= stampLen+1 || stem[stampLen] != '-' {
		return ""
	}
	return stem[stampLen+1:]
}

// loadCodexSessionTitles reads the same generated thread names that Codex uses
// in its session picker. Later entries win because renames append a new record.
func loadCodexSessionTitles(codexHome string) map[string]string {
	path := filepath.Join(resolveCodexHomeDir(codexHome), "session_index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("codex: failed to close session index", "path", path, "error", err)
		}
	}()

	titles := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	for scanner.Scan() {
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.ID != "" && strings.TrimSpace(entry.ThreadName) != "" {
			titles[entry.ID] = entry.ThreadName
		}
	}
	return titles
}

// rolloutFirstLineCwd 以最小 I/O 只读 rollout 首行，解析 session_meta 的 cwd。
// 首行缺失、不是 session_meta 或无法解析时返回 ok=false，交给完整解析路径兜底。
func rolloutFirstLineCwd(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return rolloutMetaCwd(bufio.NewReaderSize(f, 8*1024))
}

// rolloutMetaCwd 从流中读取首行并解析 session_meta.cwd。传入小缓冲 Reader，
// 让不相关的 rollout 只需付出一次小块读的开销。
func rolloutMetaCwd(r *bufio.Reader) (string, bool) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(line), &entry) != nil || entry.Type != "session_meta" {
		return "", false
	}
	return entry.Payload.Cwd, true
}

// parseCodexSessionFile reads a Codex JSONL transcript.
// Returns nil if the session's cwd doesn't match filterCwd.
func parseCodexSessionFile(path, filterCwd string) *core.AgentSessionInfo {
	// 快路径：归属只看首行 session_meta。cwd 明确且不匹配时立即返回，
	// 连 transcript 正文都不读（上游 PR #1554 的提前退出思路，这里把 I/O 也一并省掉）。
	if filterCwd != "" {
		if cwd, ok := rolloutFirstLineCwd(path); ok && cwd != "" && cwd != filterCwd {
			return nil
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}

	var sessionID string
	var sessionCwd string
	var sessionSource json.RawMessage
	var summary string
	var msgCount int
	userMsgSeen := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		switch entry.Type {
		case "session_meta":
			if sessionID != "" {
				continue
			}
			var meta struct {
				ID     string          `json:"id"`
				Cwd    string          `json:"cwd"`
				Source json.RawMessage `json:"source"`
			}
			if json.Unmarshal(entry.Payload, &meta) == nil {
				sessionID = meta.ID
				sessionCwd = meta.Cwd
				sessionSource = meta.Source
			}

		case "response_item":
			var item struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(entry.Payload, &item) == nil {
				if item.Role == "user" {
					userMsgSeen++
					msgCount++
					// The actual user prompt is the last user response_item
					// (earlier ones are system/AGENTS.md instructions).
					// Pick the last content block that looks like a real prompt.
					for _, c := range item.Content {
						if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
							summary = c.Text
						}
					}
				} else if item.Role == "assistant" {
					msgCount++
				}
			}
		}
	}

	// Filter by cwd
	if filterCwd != "" && sessionCwd != "" && sessionCwd != filterCwd {
		return nil
	}

	if sessionID == "" {
		return nil
	}
	if isSubagentSessionSource(sessionSource) {
		return nil
	}

	if len([]rune(summary)) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   stat.ModTime(),
	}
}

// isSubagentSessionSource reports whether Codex recorded the rollout as an
// internal subagent thread rather than a top-level user session.
func isSubagentSessionSource(source json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(source, &object) != nil {
		return false
	}
	_, ok := object["subagent"]
	return ok
}

// findSessionFile locates the JSONL transcript for a given session ID.
// 先用 Glob 精确命中（与 context_usage.go 的 findSessionFileInCodexHome 同法），
// 未命中再回退全目录遍历。
func findSessionFile(sessionID, codexHome string) string {
	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")
	if id := strings.TrimSpace(sessionID); id != "" && !strings.ContainsAny(id, `*?[`) {
		pattern := filepath.Join(sessionsDir, "*", "*", "*", "rollout-*"+id+".jsonl")
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			sort.Strings(matches) // 与旧的遍历顺序一致：取最早的一个
			return matches[0]
		}
	}

	var found string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) {
			found = path
		}
		return nil
	})
	return found
}

// getSessionHistory reads the JSONL transcript and returns user/assistant messages.
func getSessionHistory(sessionID, codexHome string, limit int) ([]core.HistoryEntry, error) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return nil, fmt.Errorf("session file not found for %s", sessionID)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []core.HistoryEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if raw.Type != "response_item" {
			continue
		}

		var item struct {
			Role    string `json:"role"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw.Payload, &item) != nil {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)

		switch {
		case item.Role == "user" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
					entries = append(entries, core.HistoryEntry{
						Role: "user", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Role == "assistant" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					entries = append(entries, core.HistoryEntry{
						Role: "assistant", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Type == "reasoning" && item.Text != "":
			// skip reasoning items
		}
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// patchSessionSource rewrites the session_meta line in a Codex JSONL transcript
// so that source="cli" and originator="codex_cli_rs", making the session visible
// in the interactive `codex` terminal.
func patchSessionSource(sessionID, codexHome string) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return
	}
	patchSessionSourceFile(path)
}

// patchSessionSourceFile 对已知路径的 rollout 回写 session_meta 首行。
// 与 patchSessionSource 的区别是不再查找文件，供会话列举的索引路径直接调用。
func patchSessionSourceFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return
	}
	firstLine := data[:idx]

	// Only patch if it's actually an exec-sourced session
	if !bytes.Contains(firstLine, []byte(`"source":"exec"`)) {
		return
	}

	patched := bytes.Replace(firstLine, []byte(`"source":"exec"`), []byte(`"source":"cli"`), 1)
	patched = bytes.Replace(patched, []byte(`"originator":"codex_exec"`), []byte(`"originator":"codex_cli_rs"`), 1)

	if bytes.Equal(patched, firstLine) {
		return
	}

	out := make([]byte, 0, len(patched)+len(data)-idx)
	out = append(out, patched...)
	out = append(out, data[idx:]...)

	_ = os.WriteFile(path, out, 0o644)
}

// isUserPrompt returns true if the text looks like an actual user prompt
// rather than system context (AGENTS.md, environment_context, permissions, etc.)
func isUserPrompt(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// Skip XML-style system context
	if strings.HasPrefix(t, "<") {
		return false
	}
	// Skip AGENTS.md instructions injected by Codex
	if strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "#AGENTS.md") {
		return false
	}
	return true
}
