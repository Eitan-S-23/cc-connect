package core

import (
	"log/slog"
	"strings"
)

func DefaultAttentionNotifyCfg() AttentionNotifyCfg {
	return AttentionNotifyCfg{
		OnTurnComplete: true,
		OnBlocked:      true,
		OnError:        true,
		MentionUser:    true,
	}
}

func (e *Engine) effectiveAttentionNotify() AttentionNotifyCfg {
	e.attentionNotifyMu.RLock()
	cfg := e.attentionNotify
	e.attentionNotifyMu.RUnlock()
	if e.projectState != nil {
		if enabled, overridden := e.projectState.NotifyEnabled(); overridden {
			cfg.Enabled = enabled
		}
	}
	return cfg
}

func (e *Engine) cmdNotify(p Platform, msg *Message, args []string) {
	_, inPlace := p.(AuthenticatedCardNavigable)
	e.replyWithCard(p, msg.ReplyCtx, e.notifyCommandCard(args, msg.UserID, inPlace))
}

func (e *Engine) notifyCommandCard(args []string, userID string, inPlace bool) *Card {
	e.userRolesMu.RLock()
	disabled := e.disabledCmds["notify"]
	roles := e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds["notify"]
		}
	}
	if disabled {
		return e.simpleCard(e.i18n.T(MsgNotifyTitle), "grey", e.i18n.Tf(MsgCommandDisabled, "/notify"))
	}
	feedback := ""
	if len(args) != 0 {
		if len(args) != 1 || (!strings.EqualFold(args[0], "on") && !strings.EqualFold(args[0], "off")) {
			feedback = e.i18n.T(MsgNotifyUsage)
		} else if userID == "" || !e.isAdmin(userID) {
			feedback = e.i18n.Tf(MsgAdminRequired, "/notify")
		} else {
			enabled := strings.EqualFold(args[0], "on")
			if e.projectState == nil {
				feedback = e.i18n.T(MsgNotifySaveFailed)
				slog.Error("notify: project state is unavailable", "project", e.name)
			} else if err := e.projectState.SetNotifyEnabled(enabled); err != nil {
				feedback = e.i18n.T(MsgNotifySaveFailed)
				slog.Error("notify: failed to save override", "project", e.name, "error", err)
			} else {
				feedback = e.i18n.T(MsgNotifySaved)
				slog.Info("notify: updated bot override", "project", e.name, "user_id", userID, "enabled", enabled)
			}
		}
	}
	return e.renderNotifyCard(inPlace, feedback)
}

func (e *Engine) renderNotifyCard(inPlace bool, feedback string) *Card {
	enabled := e.effectiveAttentionNotify().Enabled
	status, color := MsgNotifyOff, "grey"
	if enabled {
		status, color = MsgNotifyOn, "green"
	}
	cb := NewCard().Title(e.i18n.T(MsgNotifyTitle), color).
		Markdown(e.i18n.Tf(MsgNotifyStatus, e.name, e.i18n.T(status)))
	if feedback != "" {
		cb.Markdown(feedback)
	}
	prefix := "cmd:"
	if inPlace {
		prefix = "act:"
	}
	on, off := DefaultBtn(e.i18n.T(MsgNotifyOn), prefix+"/notify on"), DefaultBtn(e.i18n.T(MsgNotifyOff), prefix+"/notify off")
	if enabled {
		on.Type = "primary"
	} else {
		off.Type = "primary"
	}
	return cb.ButtonsEqual(on, off).Note(e.i18n.T(MsgNotifyUsage)).Build()
}

func (e *Engine) handleAuthenticatedCardNav(action, sessionKey, userID string) *Card {
	prefix, body, ok := strings.Cut(action, ":")
	if !ok {
		return nil
	}
	parts := strings.Fields(body)
	if len(parts) > 0 && parts[0] == "/autoretry" {
		switch prefix {
		case "act":
			return e.autoRetryCommandCard(parts[1:], sessionKey, userID, true)
		case "nav":
			return e.autoRetryCommandCard(nil, sessionKey, userID, true)
		default:
			return nil
		}
	}
	if len(parts) == 0 || parts[0] != "/notify" {
		return e.handleCardNav(action, sessionKey)
	}
	switch prefix {
	case "act":
		return e.notifyCommandCard(parts[1:], userID, true)
	case "nav":
		return e.notifyCommandCard(nil, userID, true)
	default:
		return nil
	}
}
