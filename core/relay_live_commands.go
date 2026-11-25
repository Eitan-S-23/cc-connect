package core

import (
	"log/slog"
	"strings"
)

func (e *Engine) relayCommandDisabled(command, userID string) bool {
	e.userRolesMu.RLock()
	defer e.userRolesMu.RUnlock()
	if e.userRoles != nil {
		if role := e.userRoles.ResolveRole(userID); role != nil {
			return role.DisabledCmds[command]
		}
	}
	return e.disabledCmds[command]
}

func (e *Engine) cmdRelay(p Platform, msg *Message, args []string) {
	_, inPlace := p.(AuthenticatedCardNavigable)
	e.replyWithCard(p, msg.ReplyCtx, e.relayCommandCard(args, msg.UserID, inPlace))
}

func (e *Engine) relayCommandCard(args []string, userID string, inPlace bool) *Card {
	if e.relayCommandDisabled("relay", userID) {
		return e.simpleCard(e.i18n.T(MsgRelayLiveTitle), "grey", e.i18n.Tf(MsgCommandDisabled, "/relay"))
	}
	if e.relayManager == nil {
		return e.simpleCard(e.i18n.T(MsgRelayLiveTitle), "grey", e.i18n.T(MsgRelayNotAvailable))
	}
	feedback := ""
	if len(args) > 0 {
		switch {
		case len(args) != 1 || (!strings.EqualFold(args[0], "on") && !strings.EqualFold(args[0], "off")):
			feedback = e.i18n.T(MsgRelayLiveUsage)
		case userID == "" || !e.isAdmin(userID):
			feedback = e.i18n.Tf(MsgAdminRequired, "/relay")
		default:
			if err := e.relayManager.SetLiveEnabled(strings.EqualFold(args[0], "on")); err != nil {
				feedback = e.i18n.T(MsgNotifySaveFailed)
				slog.Error("relay: save global switch failed", "error", err)
			} else {
				feedback = e.i18n.T(MsgRelayLiveSaved)
			}
		}
	}
	enabled := e.relayManager.LiveEnabled()
	status, color := MsgRelayLiveOff, "grey"
	if enabled {
		status, color = MsgRelayLiveOn, "green"
	}
	cb := NewCard().Title(e.i18n.T(MsgRelayLiveTitle), color).
		Markdown(e.i18n.Tf(MsgRelayLiveStatus, e.i18n.T(status)))
	if feedback != "" {
		cb.Markdown(feedback)
	}
	prefix := "cmd:"
	if inPlace {
		prefix = "act:"
	}
	on, off := DefaultBtn(e.i18n.T(MsgRelayLiveOn), prefix+"/relay on"), DefaultBtn(e.i18n.T(MsgRelayLiveOff), prefix+"/relay off")
	if enabled {
		on.Type = "primary"
	} else {
		off.Type = "primary"
	}
	return cb.ButtonsEqual(on, off).Note(e.i18n.T(MsgRelayLiveUsage)).Build()
}

func (e *Engine) handleLiveBind(p Platform, msg *Message, args []string) bool {
	rm := e.relayManager
	here := len(args) == 1 && args[0] == "here"
	if rm == nil || (!here && !rm.LiveEnabled()) {
		return false
	}
	detector, ok := p.(PrivateChatDetector)
	if !ok || !detector.IsPrivateChat(msg.ReplyCtx) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayLivePrivateOnly))
		return true
	}
	if msg.UserID == "" || !e.isAdmin(msg.UserID) || e.relayCommandDisabled("bind", msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/bind"))
		return true
	}
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgRelayLiveBindings, strings.Join(rm.liveTargets(e.name, msg.SessionKey), ", ")))
		return true
	}
	if len(args) != 1 || args[0] == "help" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayLiveBindUsage))
		return true
	}
	arg := args[0]
	inbox := relayInbox{p.Name(), msg.SessionKey, msg.UserID}
	var err error
	feedback := MsgRelayLiveBound
	switch {
	case here:
		err = rm.registerInbox(e.name, inbox)
		feedback = MsgRelayLiveInboxSaved
	case arg == "remove" || strings.HasPrefix(arg, "-"):
		err = rm.updateLive(func(s *relayLiveState) error {
			key := relaySourceKey(e.name, msg.SessionKey)
			if arg == "remove" {
				delete(s.Links, key)
			} else {
				delete(s.Links[key], strings.TrimPrefix(arg, "-"))
			}
			return nil
		})
		feedback = MsgRelayLiveUnbound
	case arg == e.name:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayBindSelf))
		return true
	default:
		rm.mu.RLock()
		_, exists := rm.live.Inboxes[arg]
		target := rm.engines[arg]
		rm.mu.RUnlock()
		if !exists || target == nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgRelayLiveNeedInbox, arg))
			return true
		}
		err = rm.bindLive(e.name, inbox, arg)
	}
	if err != nil {
		slog.Error("relay: cannot save private binding", "error", err)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNotifySaveFailed))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(feedback))
	}
	return true
}
