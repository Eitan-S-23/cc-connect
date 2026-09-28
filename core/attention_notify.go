package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 本文件实现「Agent 需要你」提醒（attention notify）。
//
// 背景：部分平台在 cc-connect 现有的完成信号上是静默的。飞书只在「新建消息」
// 且该消息真正 @ 到用户时才推送；流式卡片的增量更新和 reaction 表情都不会触发
// 推送。因此用户把手机飞书设置为「仅 @ 我的消息提醒」后，Agent 干完活或卡住等
// 人工确认时都不会响铃。
//
// 解决方式：在这些时刻额外补发一条独立短消息，消息里带一个真正生效的 @提及，
// 由平台侧可选接口 AttentionNotifier 实现。未实现该接口的平台退化为普通新消息
// （多数平台对新消息本身就会推送）。
//
// 该能力默认关闭，只有显式开启的项目才会补发消息，因此不影响任何既有部署。

// attentionNotifyTimeout bounds the extra platform call. The alert is
// best-effort: it must never stall the agent event loop, so a slow platform
// gives up long before the user notices.
const attentionNotifyTimeout = 10 * time.Second

// attentionTurn belongs to one event-loop turn. Snapshot routing before a
// queued message can replace it, and allow only one terminal notification.
type attentionTurn struct {
	engine    *Engine
	state     *interactiveState
	platform  Platform
	replyCtx  any
	userID    string
	userName  string
	startedAt time.Time
	failure   MsgKey
	finished  bool
	blocked   map[string]bool
	heartbeat *heartbeatAttention
}

func (e *Engine) newAttentionTurn(state *interactiveState, replyCtx any, startedAt time.Time) *attentionTurn {
	state.mu.Lock()
	defer state.mu.Unlock()
	n := &attentionTurn{
		engine: e, state: state, platform: state.platform, replyCtx: replyCtx,
		userID: state.currentUserID, userName: state.currentUserName,
		startedAt: startedAt, failure: MsgAttentionTurnFailed,
	}
	if state.fromHeartbeat {
		n.heartbeat = &heartbeatAttention{}
	}
	return n
}

func (n *attentionTurn) finish(success, silent bool) {
	if n == nil || n.finished {
		return
	}
	n.finished = true
	n.state.mu.Lock()
	stopped, silentStop := n.state.attentionStopRequested, n.state.attentionStopSilent
	n.state.mu.Unlock()
	if stopped {
		if silentStop {
			return
		}
		success = false
		n.failure = MsgAttentionStopped
	}
	cfg := n.engine.attentionNotify
	allowMention := n.heartbeat == nil
	if n.heartbeat != nil && success {
		if silent || !n.heartbeat.hasReply {
			return
		}
		if cfg.Enabled && cfg.OnTurnComplete && cfg.MentionUser {
			resolver, ok := n.platform.(AttentionRecipientResolver)
			if !ok {
				slog.Warn("heartbeat completion mention skipped: platform cannot resolve recipient")
			} else if userID, userName, err := resolver.ResolveAttentionRecipient(n.replyCtx); err != nil || strings.TrimSpace(userID) == "" {
				slog.Warn("heartbeat completion mention skipped: no unambiguous recipient", "platform", n.platform.Name())
			} else {
				n.userID, n.userName = userID, userName
				allowMention = true
			}
		}
	}
	if success {
		if !silent {
			n.engine.notifyTurnComplete(n.platform, n.replyCtx, n.userID, n.userName, time.Since(n.startedAt), allowMention)
		}
		return
	}
	if cfg.Enabled && cfg.OnError {
		// Raw errors may contain provider URLs, headers or credentials. The
		// normal error reply has the details; the alert only names the category.
		n.engine.notifyAttentionWithMention(n.platform, n.replyCtx, n.userID, n.userName, n.engine.i18n.T(n.failure), allowMention)
	}
}

func (n *attentionTurn) notifyBlocked(requestID, text string) {
	if n == nil || n.finished {
		return
	}
	if requestID != "" {
		if n.blocked[requestID] {
			return
		}
		if n.blocked == nil {
			n.blocked = make(map[string]bool)
		}
		n.blocked[requestID] = true
	}
	n.engine.notifyBlocked(n.platform, n.replyCtx, n.userID, n.userName, text, n.heartbeat == nil)
}

// notifyTurnComplete alerts that the agent stopped working.
//
// turnDuration feeds the MinDuration threshold so short back-and-forth
// exchanges stay quiet when the operator asked for that.
func (e *Engine) notifyTurnComplete(p Platform, replyCtx any, userID, userName string, turnDuration time.Duration, allowMention bool) {
	cfg := e.attentionNotify
	if !cfg.Enabled || !cfg.OnTurnComplete {
		return
	}
	if cfg.MinDuration > 0 && turnDuration < cfg.MinDuration {
		return
	}
	text := strings.TrimSpace(cfg.Content)
	if text == "" {
		text = e.i18n.T(MsgAttentionTurnComplete)
	}
	e.notifyAttentionWithMention(p, replyCtx, userID, userName, text, allowMention)
}

// notifyBlocked alerts that the agent stopped and waits for a human decision.
//
// text is the already-localized alert body (callers embed the reason, e.g. the
// tool that needs permission), because only the call site knows why the agent
// blocked.
func (e *Engine) notifyBlocked(p Platform, replyCtx any, userID, userName, text string, allowMention bool) {
	cfg := e.attentionNotify
	if !cfg.Enabled || !cfg.OnBlocked {
		return
	}
	e.notifyAttentionWithMention(p, replyCtx, userID, userName, text, allowMention)
}

// notifyAttention performs the actual send.
//
// The call is synchronous so ordering stays deterministic for tests, but every
// call site is already either past the end of the turn or waiting on the user,
// so the extra round-trip cannot delay any user-visible output. Failures are
// logged and never propagate: a missing alert must not break the agent turn.
func (e *Engine) notifyAttention(p Platform, replyCtx any, userID, userName, text string) {
	e.notifyAttentionWithMention(p, replyCtx, userID, userName, text, true)
}

// A turn can suppress its mention without mutating shared configuration or
// suppressing the notification itself.
func (e *Engine) notifyAttentionWithMention(p Platform, replyCtx any, userID, userName, text string, allowMention bool) {
	if p == nil || strings.TrimSpace(text) == "" {
		return
	}
	mentionUser := e.attentionNotify.MentionUser && allowMention
	if !mentionUser {
		userID, userName = "", ""
	}
	ctx, cancel := context.WithTimeout(e.ctx, attentionNotifyTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	if e.outgoingRL != nil {
		if err := e.outgoingRL.Wait(ctx, p.Name()); err != nil {
			slog.Warn("attention notify: outgoing rate limit cancelled wait", "platform", p.Name(), "error", err)
			return
		}
	}

	var err error
	notifier, supportsMention := p.(AttentionNotifier)
	if supportsMention && mentionUser {
		err = notifier.NotifyAttention(ctx, replyCtx, userID, userName, text)
	} else {
		err = p.Send(ctx, replyCtx, text)
	}
	if err != nil {
		slog.Warn("attention notify failed",
			"platform", p.Name(),
			"error", err,
			"text_len", len(text))
		return
	}
	slog.Info("attention notify sent", "platform", p.Name(), "mention_requested", supportsMention && mentionUser && userID != "")
}

// attentionBlockedPermissionText renders the alert body for a permission
// request, naming the tool the agent wants to use.
func (e *Engine) attentionBlockedPermissionText(toolName string) string {
	if toolName == "" {
		toolName = "?"
	}
	return fmt.Sprintf(e.i18n.T(MsgAttentionBlockedPermission), toolName)
}
