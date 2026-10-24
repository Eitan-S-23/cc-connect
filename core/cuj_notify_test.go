package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCUJ_N1_NotifyAcrossConversationsAndBots(t *testing.T) {
	makeBot := func(name string) (*Engine, *attentionRecordingPlatform) {
		t.Helper()
		dir := t.TempDir()
		p := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
		e := NewEngine(name, &cujAgent{}, []Platform{p}, filepath.Join(dir, "sessions.json"), LangEnglish)
		e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
		e.SetAdminFrom("admin")
		cfg := DefaultAttentionNotifyCfg()
		cfg.Enabled = true
		e.SetAttentionNotify(cfg)
		t.Cleanup(func() { _ = e.Stop() })
		return e, p
	}
	a, pa := makeBot("bot-a")
	b, pb := makeBot("bot-b")
	send := func(e *Engine, p *attentionRecordingPlatform, key, text string) {
		t.Helper()
		e.ReceiveMessage(p, &Message{
			SessionKey: key, Platform: "test", UserID: "admin", UserName: "Admin",
			Content: text, ReplyCtx: key,
		})
	}
	send(a, pa, "test:chat-1:admin", "/notify")
	if sent := strings.Join(pa.getSent(), "\n"); !strings.Contains(sent, "bot-a") || !strings.Contains(sent, "/notify on|off") {
		t.Fatalf("missing initial status: %s", sent)
	}
	send(a, pa, "test:chat-2:admin", "/notify off")
	if sent := strings.Join(pa.getSent(), "\n"); !strings.Contains(sent, a.i18n.T(MsgNotifySaved)) {
		t.Fatalf("off did not confirm success: %s", sent)
	}
	pa.clearSent()
	send(a, pa, "test:chat-1:admin", "do the task")
	waitAttentionSessionIdle(t, a, "test:chat-1:admin")
	if sent := strings.Join(pa.getSent(), "\n"); !strings.Contains(sent, "ok") {
		t.Fatalf("normal reply disappeared when notify was off: %s", sent)
	}
	if len(pa.getNotifications()) != 0 {
		t.Fatal("off in chat-2 did not suppress alerts in chat-1")
	}
	send(b, pb, "test:chat-1:admin", "do another task")
	if got := waitForAttention(t, pb, 1); len(got) != 1 {
		t.Fatal("bot-b stopped notifying when bot-a was disabled")
	}
	send(a, pa, "test:chat-2:admin", "/notify on")
	send(a, pa, "test:chat-1:admin", "do the next task")
	if got := waitForAttention(t, pa, 1); len(got) != 1 {
		t.Fatal("on did not restore bot-a's attention alerts")
	}
}
