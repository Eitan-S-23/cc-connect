package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// P1 会话连续性用例：会话 ID 的落盘时机。
//
// 故障形态（用户实测）：新会话里一轮请求因 API 错误（限流等）失败后，下一轮
// 请求没有续接原会话，而是静默开了新会话，整段上下文无声丢失。根因是 ID 只在
// 成功路径上落盘：Codex 的 thread.started 仅存内存，失败事件又不带 ID，于是
// 下一轮读到的 agent_session_id 是空的，只能当新会话处理。
//
// 测试用事件投递时点完全由测试掌握的会话桩，避免 goroutine 时序抖动。注意引擎
// 会在回合开始前 drainEvents 排空通道里上一轮的残留事件（engine.go drainEvents），
// 因此事件必须等 Send 被调用之后再投递。

// p1Session 是 P1 专用会话桩：CurrentSessionID 可控（模拟 agent 在回合中途才
// 得知自己的线程 ID），事件由测试直接投递。
type p1Session struct {
	events    chan Event
	sent      chan struct{}
	sendOnce  sync.Once
	closeOnce sync.Once
	closed    atomic.Bool
	mu        sync.Mutex
	id        string
}

func newP1Session(id string) *p1Session {
	return &p1Session{events: make(chan Event, 16), sent: make(chan struct{}), id: id}
}

func (s *p1Session) Send(string, string, []ImageAttachment, []FileAttachment) error {
	s.sendOnce.Do(func() { close(s.sent) })
	return nil
}
func (s *p1Session) RespondPermission(string, PermissionResult) error { return nil }
func (s *p1Session) Events() <-chan Event                             { return s.events }
func (s *p1Session) CurrentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
func (s *p1Session) setID(id string) {
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
}
func (s *p1Session) Alive() bool { return !s.closed.Load() }
func (s *p1Session) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.events)
	})
	return nil
}

type p1Agent struct{ session *p1Session }

func (a *p1Agent) Name() string { return "p1" }
func (a *p1Agent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}
func (a *p1Agent) ListSessions(context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *p1Agent) Stop() error                                              { return nil }

func newP1Engine(t *testing.T, agent Agent) (*Engine, *attentionRecordingPlatform) {
	t.Helper()
	p := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("p1", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return e, p
}

const p1SessionKey = "test:p1"

// startP1Turn 触发一个回合，等到引擎真正开始消费事件（Send 已调用，陈旧事件
// 已排空）后返回引擎侧的会话对象。
func startP1Turn(t *testing.T, e *Engine, p Platform, sess *p1Session) *Session {
	t.Helper()
	e.ReceiveMessage(p, &Message{
		SessionKey: p1SessionKey, Platform: "test", MessageID: "m1",
		UserID: "user1", UserName: "user1", Content: "do the work", ReplyCtx: "m1",
	})
	s := e.sessions.GetOrCreateActive(p1SessionKey)
	select {
	case <-sess.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("回合没有开始（Send 未被调用）")
	}
	return s
}

// pushP1Event 投递一个事件，超时即判定为引擎已不再消费。
func pushP1Event(t *testing.T, s *p1Session, evt Event) {
	t.Helper()
	select {
	case s.events <- evt:
	case <-time.After(3 * time.Second):
		t.Fatalf("投递事件 %s 超时", evt.Type)
	}
}

// waitP1Persisted 等待会话 ID 落盘（引擎在独立 goroutine 中处理事件）。
func waitP1Persisted(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetAgentSessionID() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("会话 ID = %q, 期望落盘为 %q", s.GetAgentSessionID(), want)
}

func lastSentMessage(t *testing.T, p *attentionRecordingPlatform) string {
	t.Helper()
	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("平台没有收到任何消息")
	}
	return sent[len(sent)-1]
}

// A1：agent 在回合结束前上报会话 ID 时必须立即落盘——断言落盘发生时回合仍在
// 进行中，证明落盘不依赖回合成功。
func TestEventSessionStartedPersistsIDMidTurn(t *testing.T) {
	cases := []struct {
		name string
		// recordFirst 模拟 Codex 的真实顺序：先把线程 ID 记到 agent 侧，
		// 再上报事件；事件里的 ID 与 agent 侧一致。
		recordFirst bool
	}{
		{name: "事件携带会话 ID", recordFirst: false},
		{name: "agent 侧已先记录线程 ID", recordFirst: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newP1Session("")
			e, p := newP1Engine(t, &p1Agent{session: sess})
			s := startP1Turn(t, e, p, sess)

			if tc.recordFirst {
				sess.setID("thread-early")
			}
			pushP1Event(t, sess, Event{Type: EventSessionStarted, SessionID: "thread-early"})
			waitP1Persisted(t, s, "thread-early")
			if !s.Busy() {
				t.Fatal("落盘时回合已经结束，无法证明“回合未结束即落盘”")
			}
		})
	}
}

// A2：回合失败也要落盘。会话 ID 在回合中途才产生（模拟 Codex 的 thread.started
// 之后才知道线程 ID），且失败事件本身不带 ID。
func TestEventErrorPersistsSessionIDForResume(t *testing.T) {
	sess := newP1Session("")
	e, p := newP1Engine(t, &p1Agent{session: sess})
	s := startP1Turn(t, e, p, sess)

	sess.setID("thread-known")
	pushP1Event(t, sess, Event{Type: EventError, Error: errors.New("429 rate limit exceeded")})
	waitP1Persisted(t, s, "thread-known")
	waitAttentionSessionIdle(t, e, p1SessionKey)

	got := lastSentMessage(t, p)
	if !strings.Contains(got, e.i18n.T(MsgErrorResumeNext)) {
		t.Fatalf("失败回执没有说明“下一条会续接本会话”：\n%s", got)
	}
	if !strings.Contains(got, "429 rate limit exceeded") {
		t.Fatalf("失败回执没有带上失败原因：\n%s", got)
	}
}

// A3：不可恢复错误（模型不存在 / 会话已不存在）要如实说下一条会开新会话，
// 不能让用户以为上下文还在。
func TestEventErrorReceiptUnrecoverableSaysFreshSession(t *testing.T) {
	for _, errMsg := range []string{
		"unrecognized_model: gpt-6-astra",
		"Session not found",
	} {
		t.Run(errMsg, func(t *testing.T) {
			sess := newP1Session("")
			e, p := newP1Engine(t, &p1Agent{session: sess})
			startP1Turn(t, e, p, sess)

			sess.setID("thread-known")
			pushP1Event(t, sess, Event{Type: EventError, Error: errors.New(errMsg)})
			waitAttentionSessionIdle(t, e, p1SessionKey)

			got := lastSentMessage(t, p)
			if !strings.Contains(got, e.i18n.T(MsgErrorFreshNext)) {
				t.Fatalf("不可恢复错误的回执应说明下一条会开新会话：\n%s", got)
			}
		})
	}
}

// 用户主动停止不是故障：这些命令会刻意清掉会话 ID，失败路径不能把它写回去，
// 否则被丢弃的会话会被复活。
func TestEventErrorCancelDoesNotResurrectSessionID(t *testing.T) {
	sess := newP1Session("")
	e, p := newP1Engine(t, &p1Agent{session: sess})
	s := startP1Turn(t, e, p, sess)

	sess.setID("thread-known")
	pushP1Event(t, sess, Event{Type: EventError, Error: context.Canceled})
	waitAttentionSessionIdle(t, e, p1SessionKey)

	if got := s.GetAgentSessionID(); got != "" {
		t.Fatalf("取消后的会话 ID = %q, 期望保持为空（不能复活已丢弃的会话）", got)
	}
	if got := lastSentMessage(t, p); !strings.Contains(got, e.i18n.T(MsgErrorFreshNext)) {
		t.Fatalf("取消后没有可续接的会话，回执应说明下一条会开新会话：\n%s", got)
	}
}
