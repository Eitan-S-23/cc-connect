package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// attentionRecordingPlatform 记录 NotifyAttention 的调用参数，用于断言提醒内容
// 与 @ 对象。它同时实现了 AttentionNotifier，模拟飞书这类「只有新建消息 + 真
// 实 @提及才会推送」的平台。
type attentionRecordingPlatform struct {
	stubPlatformEngine
	mu            sync.Mutex
	notifications []attentionCall
	notifyError   error
}

type attentionCall struct {
	replyCtx any
	userID   string
	userName string
	text     string
}

func (p *attentionRecordingPlatform) NotifyAttention(_ context.Context, replyCtx any, userID, userName, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifications = append(p.notifications, attentionCall{replyCtx: replyCtx, userID: userID, userName: userName, text: text})
	return p.notifyError
}

func (p *attentionRecordingPlatform) getNotifications() []attentionCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]attentionCall, len(p.notifications))
	copy(out, p.notifications)
	return out
}

// waitForAttention 轮询等待提醒出现，避免测试依赖固定的 sleep 时长。
func waitForAttention(t *testing.T, p *attentionRecordingPlatform, want int) []attentionCall {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := p.getNotifications(); len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p.getNotifications()
}

// newAttentionTestEngine 构造一个已挂好提醒配置的引擎，并返回参与测试的组件。
func newAttentionTestEngine(t *testing.T, plat *attentionRecordingPlatform, cfg AttentionNotifyCfg, lang Language) *Engine {
	t.Helper()
	e := NewEngine("test", &cujAgent{}, []Platform{plat}, t.TempDir()+"/sessions.json", lang)
	e.SetAttentionNotify(cfg)
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

// 用户发来一条消息、Agent 正常答完后，必须补发一条带 @提及的提醒。
func TestAttentionNotifyTurnCompleteMentionsSender(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := newAttentionTestEngine(t, plat, AttentionNotifyCfg{
		Enabled:        true,
		OnTurnComplete: true,
		MentionUser:    true,
	}, LangChinese)

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:alice", Platform: "test", MessageID: "m1",
		UserID: "ou_alice", UserName: "Alice", Content: "帮我改个 bug", ReplyCtx: "ctx",
	})

	got := waitForAttention(t, plat, 1)
	if len(got) != 1 {
		t.Fatalf("提醒条数 = %d, want 1（Agent 答完必须提醒一次）", len(got))
	}
	if got[0].userID != "ou_alice" {
		t.Fatalf("提醒的 @对象 = %q, want %q", got[0].userID, "ou_alice")
	}
	if got[0].userName != "Alice" {
		t.Fatalf("提醒的 @名字 = %q, want %q", got[0].userName, "Alice")
	}
	if got[0].text != "🔔 完成 —— Agent 已结束本轮工作。" {
		t.Fatalf("提醒文案 = %q，与 i18n 中文默认值不一致", got[0].text)
	}
}

// 未开启该功能时必须完全不发提醒，保证既有部署行为不变。
func TestAttentionNotifyDisabledByDefault(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &cujAgent{}, []Platform{plat}, t.TempDir()+"/sessions.json", LangChinese)

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:bob", Platform: "test", MessageID: "m1",
		UserID: "ou_bob", UserName: "Bob", Content: "hi", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(plat.getSent()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := plat.getNotifications(); len(got) != 0 {
		t.Fatalf("默认配置下发出了 %d 条提醒，want 0（功能必须默认关闭）", len(got))
	}
}

// 只开「阻塞提醒」时，正常答完不应提醒；被权限请求卡住时必须提醒。
func TestAttentionNotifyBlockedOnPermissionRequest(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	agent.nextSessionEvents = []Event{
		{Type: EventPermissionRequest, RequestID: "req-bash", ToolName: "Bash"},
		{Type: EventResult, Content: "done", Done: true},
	}
	e := NewEngine("test", agent, []Platform{plat}, t.TempDir()+"/sessions.json", LangChinese)
	e.SetAttentionNotify(AttentionNotifyCfg{Enabled: true, OnBlocked: true, MentionUser: true})

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:carol", Platform: "test", MessageID: "m1",
		UserID: "ou_carol", UserName: "Carol", Content: "删掉临时目录", ReplyCtx: "ctx",
	})

	got := waitForAttention(t, plat, 1)
	if len(got) != 1 {
		t.Fatalf("提醒条数 = %d, want 1（Agent 卡在权限确认必须提醒）", len(got))
	}
	if got[0].userID != "ou_carol" {
		t.Fatalf("提醒的 @对象 = %q, want %q", got[0].userID, "ou_carol")
	}
	if !strings.Contains(got[0].text, "Bash") {
		t.Fatalf("提醒文案 %q 未包含待授权工具名 Bash", got[0].text)
	}
	if !strings.Contains(got[0].text, "等待你处理") {
		t.Fatalf("提醒文案 %q 不是阻塞提醒（i18n 中文默认值）", got[0].text)
	}

	// 放行，避免事件循环一直阻塞在 <-pending.Resolved 上。
	resolvePendingPermission(t, e, "test:carol")
	waitAttentionSessionIdle(t, e, "test:carol")
	_ = e.Stop()
}

// AskUserQuestion 走的是同一条阻塞分支，但文案不同：它不是在要授权，而是提问。
func TestAttentionNotifyBlockedOnAskUserQuestion(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	agent.nextSessionEvents = []Event{
		{
			Type:      EventPermissionRequest,
			RequestID: "req-ask",
			ToolName:  "AskUserQuestion",
			Questions: []UserQuestion{{Question: "用哪个数据库？", Options: []UserQuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}},
		},
		{Type: EventResult, Content: "done", Done: true},
	}
	e := NewEngine("test", agent, []Platform{plat}, t.TempDir()+"/sessions.json", LangChinese)
	e.SetAttentionNotify(AttentionNotifyCfg{Enabled: true, OnBlocked: true, OnTurnComplete: true, MentionUser: true})

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:dave", Platform: "test", MessageID: "m1",
		UserID: "ou_dave", UserName: "Dave", Content: "建个表", ReplyCtx: "ctx",
	})

	got := waitForAttention(t, plat, 1)
	if len(got) != 1 {
		t.Fatalf("提醒条数 = %d, want 1（Agent 提问必须提醒）", len(got))
	}
	if got[0].userID != "ou_dave" {
		t.Fatalf("提醒的 @对象 = %q, want %q", got[0].userID, "ou_dave")
	}
	if !strings.Contains(got[0].text, "向你提问") {
		t.Fatalf("提醒文案 %q 不是提问提醒（i18n 中文默认值）", got[0].text)
	}
	if strings.Contains(got[0].text, "AskUserQuestion") {
		t.Fatalf("提醒文案 %q 泄露了内部工具名，用户看不懂", got[0].text)
	}

	resolvePendingPermission(t, e, "test:dave")
	got = waitForAttention(t, plat, 2)
	if len(got) != 2 || got[1].userID != "ou_dave" || !strings.Contains(got[1].text, "完成") {
		t.Fatalf("answering the question must be followed by one mentioned completion notice: %+v", got)
	}
	waitAttentionSessionIdle(t, e, "test:dave")
	_ = e.Stop()
}

// MinDuration 用于压掉短回合的提醒；调小后同一会话应重新收到提醒。
func TestAttentionNotifyMinDurationSkipsShortTurn(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := newAttentionTestEngine(t, plat, AttentionNotifyCfg{
		Enabled:        true,
		OnTurnComplete: true,
		MentionUser:    true,
		MinDuration:    time.Hour,
	}, LangChinese)

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:erin", Platform: "test", MessageID: "m1",
		UserID: "ou_erin", UserName: "Erin", Content: "hi", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(plat.getSent()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := plat.getNotifications(); len(got) != 0 {
		t.Fatalf("MinDuration=1h 时短回合仍发出 %d 条提醒，want 0", len(got))
	}

	// 同一会话再走一轮，这次不设时长门槛。
	waitAttentionSessionIdle(t, e, "test:erin")
	e.SetAttentionNotify(AttentionNotifyCfg{Enabled: true, OnTurnComplete: true, MentionUser: true})
	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:erin", Platform: "test", MessageID: "m2",
		UserID: "ou_erin", UserName: "Erin", Content: "再试一次", ReplyCtx: "ctx",
	})
	if got := waitForAttention(t, plat, 1); len(got) != 1 {
		t.Fatalf("取消时长门槛后提醒条数 = %d, want 1", len(got))
	}
}

// 自定义文案应覆盖默认文案。
func TestAttentionNotifyCustomContent(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := newAttentionTestEngine(t, plat, AttentionNotifyCfg{
		Enabled:        true,
		OnTurnComplete: true,
		MentionUser:    true,
		Content:        "活干完了，来看看",
	}, LangChinese)

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:frank", Platform: "test", MessageID: "m1",
		UserID: "ou_frank", UserName: "Frank", Content: "hi", ReplyCtx: "ctx",
	})

	got := waitForAttention(t, plat, 1)
	if len(got) != 1 || got[0].text != "活干完了，来看看" {
		t.Fatalf("提醒 = %+v, want 自定义文案", got)
	}
}

// 关掉 mention_user 时，提醒仍要发出，但不再 @ 任何人。
func TestAttentionNotifyMentionOptOut(t *testing.T) {
	plat := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := newAttentionTestEngine(t, plat, AttentionNotifyCfg{
		Enabled:        true,
		OnTurnComplete: true,
		MentionUser:    false,
	}, LangChinese)

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:grace", Platform: "test", MessageID: "m1",
		UserID: "ou_grace", UserName: "Grace", Content: "hi", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, sent := range plat.getSent() {
			if strings.Contains(sent, "完成") {
				if got := plat.getNotifications(); len(got) != 0 {
					t.Fatalf("mention_user=false called mention notifier %d times", len(got))
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mention_user=false did not send an ordinary completion notice")
}

// 平台未实现 AttentionNotifier 时应退化为普通新消息，而不是静默丢弃提醒。
func TestAttentionNotifyFallsBackToPlainSend(t *testing.T) {
	plat := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &cujAgent{}, []Platform{plat}, t.TempDir()+"/sessions.json", LangChinese)
	e.SetAttentionNotify(AttentionNotifyCfg{Enabled: true, OnTurnComplete: true, MentionUser: true})

	e.ReceiveMessage(plat, &Message{
		SessionKey: "test:heidi", Platform: "test", MessageID: "m1",
		UserID: "ou_heidi", UserName: "Heidi", Content: "hi", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range plat.getSent() {
			if strings.Contains(s, "完成") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("平台未实现 AttentionNotifier 时未发出普通提醒消息，已发送: %v", plat.getSent())
}

// resolvePendingPermission 释放事件循环里等待中的权限请求，避免测试用例把
// goroutine 永久阻塞在 <-pending.Resolved 上。
func resolvePendingPermission(t *testing.T, e *Engine, sessionKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.interactiveMu.Lock()
		state := e.interactiveStates[sessionKey]
		e.interactiveMu.Unlock()
		if state != nil {
			state.mu.Lock()
			pending := state.pending
			state.mu.Unlock()
			if pending != nil {
				pending.resolve()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("会话 %s 上找不到等待中的权限请求，无法释放事件循环", sessionKey)
}
