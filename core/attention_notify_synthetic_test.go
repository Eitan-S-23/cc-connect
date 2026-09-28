package core

import (
	"errors"
	"testing"
	"time"
)

// 定时任务注入的是合成消息（UserID = syntheticTimerUserID），提醒必须 @ 到
// 会话归属人：占位身份不是合法 open_id，平台会拒绝发送并丢弃整条提醒。
func TestTimerTurnMentionsSessionOwner(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Done: true, Content: "scheduled check finished",
	}}})
	job := &TimerJob{
		ID: "job-1", Project: "heartbeat-attention", SessionKey: "test:oc_chat:ou_owner",
		Prompt: "run the scheduled check", Description: "scheduled check", CreatedAt: time.Now(),
	}
	if err := e.ExecuteTimerJob(job); err != nil {
		t.Fatal(err)
	}
	notices := waitForAttention(t, &p.attentionRecordingPlatform, 1)
	if len(notices) != 1 || notices[0].userID != "ou_owner" || notices[0].text != e.i18n.T(MsgAttentionTurnComplete) {
		t.Fatalf("timer completion must mention the session owner: %+v", notices)
	}
}

// cron 与 webhook 注入的回合和定时任务同属一类，收件人同样来自会话键。
func TestCronTurnMentionsSessionOwner(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Done: true, Content: "cron job finished",
	}}})
	job := &CronJob{
		ID: "cron-1", Project: "heartbeat-attention", SessionKey: "test:oc_chat:ou_owner",
		Prompt: "summarize activity", Description: "Daily summary",
	}
	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatal(err)
	}
	notices := waitForAttention(t, &p.attentionRecordingPlatform, 1)
	if len(notices) != 1 || notices[0].userID != "ou_owner" || notices[0].text != e.i18n.T(MsgAttentionTurnComplete) {
		t.Fatalf("cron completion must mention the session owner: %+v", notices)
	}
}

func TestWebhookTurnMentionsSessionOwner(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Done: true, Content: "webhook prompt handled",
	}}})
	ws := &WebhookServer{}
	ws.executePrompt(e, "test:oc_chat:ou_owner", "run the release checklist", true, "release")
	notices := waitForAttention(t, &p.attentionRecordingPlatform, 1)
	if len(notices) != 1 || notices[0].userID != "ou_owner" || notices[0].text != e.i18n.T(MsgAttentionTurnComplete) {
		t.Fatalf("webhook completion must mention the session owner: %+v", notices)
	}
}

// 真实发送者的优先级高于会话归属人：群会话里提问的人未必是会话归属人，
// 不能被解析结果覆盖。
func TestRealSenderKeepsPrecedenceOverSessionOwner(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{})
	e.notifyAttention(p, "test:oc_chat:ou_owner", "ou_sender", "Sender", "alert")
	got := p.getNotifications()
	if len(got) != 1 || got[0].userID != "ou_sender" {
		t.Fatalf("real sender must keep precedence over the session owner: %+v", got)
	}
}

// 平台未实现 AttentionRecipientResolver 时保持原状，不猜测收件人。
func TestSyntheticTurnWithoutResolverKeepsIdentity(t *testing.T) {
	p := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := newAttentionTestEngine(t, p, AttentionNotifyCfg{
		Enabled: true, OnError: true, MentionUser: true,
	}, LangEnglish)
	e.notifyAttention(p, "test:oc_chat:ou_owner", syntheticTimerUserID, syntheticTimerUserID, "alert")
	got := p.getNotifications()
	if len(got) != 1 || got[0].userID != syntheticTimerUserID {
		t.Fatalf("platform without a resolver must keep the original identity: %+v", got)
	}
}

// 共享会话/线程键解析不出唯一归属人时同样保持原状。
func TestSyntheticTurnResolverFailureKeepsIdentity(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{})
	p.recipientErr = errors.New("test: no unambiguous attention recipient")
	e.notifyAttention(p, "test:oc_chat:thread:om_root", syntheticHeartbeatUserID, syntheticHeartbeatUserID, "alert")
	got := p.getNotifications()
	if len(got) != 1 || got[0].userID != syntheticHeartbeatUserID {
		t.Fatalf("unresolvable recipient must keep the original identity: %+v", got)
	}
}

// 合成占位身份与空身份的判定：只有这两类才触发归属人解析。
func TestIsSyntheticAttentionUserID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"", true},
		{"   ", true},
		{syntheticTimerUserID, true},
		{syntheticCronUserID, true},
		{syntheticHeartbeatUserID, true},
		{syntheticWebhookUserID, true},
		{syntheticWebAdminUserID, true},
		{"ou_alice", false},
		{"ou_c804edc325735cad3f628919dfd96d4a", false},
	} {
		if got := isSyntheticAttentionUserID(tc.id); got != tc.want {
			t.Fatalf("isSyntheticAttentionUserID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
