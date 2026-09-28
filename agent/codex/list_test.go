package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentListSessions_ExcludesSubagentRollouts(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "03")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}
	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}

	writeRollout := func(name, sessionID, source string) {
		t.Helper()
		body := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":` + source + `}}` + "\n" +
			`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"fix the login bug"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write rollout %s: %v", name, err)
		}
	}

	writeRollout("rollout-top-level.jsonl", "top-level", `"vscode"`)
	writeRollout(
		"rollout-subagent.jsonl",
		"subagent",
		`{"subagent":{"thread_spawn":{"parent_thread_id":"top-level"}}}`,
	)

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1 top-level session", len(sessions))
	}
	if sessions[0].ID != "top-level" {
		t.Fatalf("ListSessions()[0].ID = %q, want %q", sessions[0].ID, "top-level")
	}
}

func TestAgentListSessions_ExcludesSubagentRolloutWithCopiedParentMeta(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "04")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	parentMeta := `{"type":"session_meta","payload":{"id":"parent","cwd":` + string(workDirJSON) + `,"source":"vscode"}}`
	parentRollout := parentMeta + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"top-level prompt"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-parent.jsonl"), []byte(parentRollout), 0o644); err != nil {
		t.Fatalf("write parent rollout: %v", err)
	}

	childMeta := `{"type":"session_meta","payload":{"id":"child","cwd":` + string(workDirJSON) + `,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`
	childRollout := childMeta + "\n" + parentMeta + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"copied parent prompt"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-child.jsonl"), []byte(childRollout), 0o644); err != nil {
		t.Fatalf("write child rollout: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want only the parent session", len(sessions))
	}
	if sessions[0].ID != "parent" {
		t.Fatalf("ListSessions()[0].ID = %q, want parent", sessions[0].ID)
	}
}

func TestAgentListSessions_UsesSessionIndexThreadName(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "04")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e71"
	rollout := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"这是很长的具体需求正文，不应该覆盖 Codex 生成的会话名称"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-session.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	indexEntry := `{"id":"` + sessionID + `","thread_name":"设计简易基础管理模块","updated_at":"2026-08-03T06:01:25Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(indexEntry), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].Summary != "设计简易基础管理模块" {
		t.Fatalf("ListSessions()[0].Summary = %q, want Codex thread name", sessions[0].Summary)
	}
}

func TestAgentListSessions_LongThreadNameTruncated(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e72"
	rollout := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-session.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	longTitle := strings.Repeat("会", 61)
	indexEntry := `{"id":"` + sessionID + `","thread_name":"` + longTitle + `","updated_at":"2026-08-15T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(indexEntry), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	want := strings.Repeat("会", 60) + "..."
	if sessions[0].Summary != want {
		t.Fatalf("ListSessions()[0].Summary = %q, want %q", sessions[0].Summary, want)
	}
}

// firstReadOnlyReader 只在第一次 Read 返回首行，之后被读取即报错，
// 用于证明不相关的 rollout 不会被读到正文。
type firstReadOnlyReader struct {
	line  string
	reads int
}

func (r *firstReadOnlyReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return copy(p, r.line), nil
	}
	return 0, errors.New("不应读取 transcript 正文")
}

func TestRolloutMetaCwd_UnrelatedRolloutOnlyReadsFirstLine(t *testing.T) {
	reader := &firstReadOnlyReader{line: `{"type":"session_meta","payload":{"id":"other","cwd":"/workspace/other"}}` + "\n"}

	cwd, ok := rolloutMetaCwd(bufio.NewReaderSize(reader, 8*1024))
	if !ok {
		t.Fatal("首行为 session_meta 时 ok 应为 true")
	}
	if cwd != "/workspace/other" {
		t.Fatalf("cwd = %q, want /workspace/other", cwd)
	}
	if reader.reads != 1 {
		t.Fatalf("底层读取次数 = %d, 期望 1（只读首行）", reader.reads)
	}
}

func TestParseCodexSessionFile_MalformedFirstLineFallsBackToFullParse(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("创建会话目录: %v", err)
	}
	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("编码工作目录: %v", err)
	}

	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e73"
	body := "not valid json\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"修复登录缺陷"}]}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"done"}]}}` + "\n"
	path := filepath.Join(sessionsDir, "rollout-2026-09-28T10-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写入 rollout: %v", err)
	}

	info := parseCodexSessionFile(path, workDir)
	if info == nil {
		t.Fatal("首行损坏时应回退到完整解析，不能返回 nil")
	}
	if info.ID != sessionID {
		t.Errorf("ID = %q, want %q", info.ID, sessionID)
	}
	if info.Summary != "修复登录缺陷" {
		t.Errorf("Summary = %q, want 用户提示词", info.Summary)
	}
	if info.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", info.MessageCount)
	}
}

func TestParseCodexSessionFile_NonMatchingCwdReturnsNil(t *testing.T) {
	workDir := t.TempDir()
	otherDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("创建会话目录: %v", err)
	}
	otherJSON, err := json.Marshal(otherDir)
	if err != nil {
		t.Fatalf("编码目录: %v", err)
	}

	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e74"
	body := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(otherJSON) + `,"source":"vscode"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"别的项目"}]}}` + "\n"
	path := filepath.Join(sessionsDir, "rollout-2026-09-28T10-01-00-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写入 rollout: %v", err)
	}

	if info := parseCodexSessionFile(path, workDir); info != nil {
		t.Fatalf("cwd 不匹配时应返回 nil，得到 %+v", info)
	}
	// 按文件自身的 cwd 过滤时仍应完整解析
	info := parseCodexSessionFile(path, otherDir)
	if info == nil {
		t.Fatal("按自身 cwd 过滤时不应返回 nil")
	}
	if info.ID != sessionID {
		t.Fatalf("ID = %q, want %q", info.ID, sessionID)
	}
}

func TestSessionIDFromRolloutName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"rollout-2026-09-18T23-43-01-01a0b52f-73c6-7c11-835a-d1003a24e0c9.jsonl", "01a0b52f-73c6-7c11-835a-d1003a24e0c9"},
		{"rollout-2026-01-02T00-00-00-abc.jsonl", "abc"},
		{"session.jsonl", ""},
		{"rollout-2026-01-02T00-00-00.jsonl", ""},
		{"rollout-2026-01-02T00-00-00abc.jsonl", ""},
		{"rollout-2026-01-02T00-00-00-abc.json", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := sessionIDFromRolloutName(tc.name); got != tc.want {
			t.Errorf("sessionIDFromRolloutName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestListCodexSessions_PatchesSourceThroughIndex(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("创建会话目录: %v", err)
	}
	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("编码工作目录: %v", err)
	}

	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e75"
	body := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"exec","originator":"codex_exec"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"索引回写验证"}]}}` + "\n"
	path := filepath.Join(sessionsDir, "rollout-2026-09-28T11-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写入 rollout: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() 错误: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() 返回 %d 个会话，期望 1", len(sessions))
	}

	patched, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("回读 rollout: %v", err)
	}
	if !strings.Contains(string(patched), `"source":"cli"`) {
		t.Fatalf("source 未回写为 cli: %s", patched)
	}
	if !strings.Contains(string(patched), `"originator":"codex_cli_rs"`) {
		t.Fatalf("originator 未回写为 codex_cli_rs: %s", patched)
	}
}

func TestListCodexSessions_CancelledContextReturnsError(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("创建会话目录: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	if _, err := agent.ListSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, 期望 context.Canceled", err)
	}
}
