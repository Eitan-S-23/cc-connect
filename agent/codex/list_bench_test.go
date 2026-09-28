package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 合成语料规模。命中占比与真实故障场景接近：命中会话越多，旧实现
// 的“每个命中会话再全目录重扫一次”越致命（成本 ≈ 命中数 × 文件总数）。
const (
	benchTotalFiles   = 800
	benchMatchedFiles = 200
	benchLinesPerFile = 40
)

// benchCorpus 生成 800 个 rollout（约 30KB/文件）：前 200 个属于 workDir，
// 其余属于另一个目录，用来度量“过滤不相关会话”的开销。
func benchCorpus(b *testing.B) (workDir, codexHome string) {
	b.Helper()
	codexHome = b.TempDir()
	workDir = filepath.Join(b.TempDir(), "workspace")
	otherDir := filepath.Join(b.TempDir(), "other")
	for _, dir := range []string{workDir, otherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
	}
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		b.Fatal(err)
	}

	filler := strings.Repeat("x", 680)
	var body strings.Builder
	for i := 0; i < benchLinesPerFile; i++ {
		body.WriteString(`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"` + filler + `"}]}}` + "\n")
	}

	for i := 0; i < benchTotalFiles; i++ {
		cwd := otherDir
		if i < benchMatchedFiles {
			cwd = workDir
		}
		cwdJSON, err := json.Marshal(cwd)
		if err != nil {
			b.Fatal(err)
		}
		id := fmt.Sprintf("019fc636-3567-76e3-%04d", i)
		var sb strings.Builder
		sb.WriteString(`{"type":"session_meta","payload":{"id":"` + id + `","cwd":` + string(cwdJSON) + `,"source":"exec"}}` + "\n")
		sb.WriteString(body.String())
		name := fmt.Sprintf("rollout-2026-09-28T10-%02d-%02d-%s.jsonl", i/60, i%60, id)
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return workDir, codexHome
}

// BenchmarkListSessions 度量一次完整会话列举。用 -benchtime=1x 时包含
// 命中会话的 source 回写；多次迭代时回写只发生一次（首轮已改完）。
func BenchmarkListSessions(b *testing.B) {
	workDir, codexHome := benchCorpus(b)
	agent := &Agent{workDir: workDir, codexHome: codexHome}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sessions, err := agent.ListSessions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(sessions) != benchMatchedFiles {
			b.Fatalf("ListSessions() 返回 %d 个会话，期望 %d 个", len(sessions), benchMatchedFiles)
		}
	}
}
