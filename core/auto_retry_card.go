package core

import (
	"log/slog"
	"strings"
	"time"
)

func (e *Engine) autoRetryCommandCard(args []string, sessionKey, userID string, inPlace bool) *Card {
	e.userRolesMu.RLock()
	disabled, roles := e.disabledCmds["autoretry"], e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds["autoretry"]
		}
	}
	if disabled {
		return e.simpleCard(e.i18n.T(MsgAutoRetryTitle), "grey", e.i18n.Tf(MsgCommandDisabled, "/autoretry"))
	}
	feedback := ""
	if len(args) > 0 {
		if len(args) != 1 || (!strings.EqualFold(args[0], "on") && !strings.EqualFold(args[0], "off")) {
			feedback = e.i18n.T(MsgAutoRetryUsage)
		} else if userID == "" || !e.isAdmin(userID) {
			feedback = e.i18n.Tf(MsgAdminRequired, "/autoretry")
		} else if e.projectState == nil {
			feedback = e.i18n.T(MsgNotifySaveFailed)
		} else {
			enabled := strings.EqualFold(args[0], "on")
			if err := e.projectState.SetAutoRetryEnabled(enabled); err != nil {
				feedback = e.i18n.T(MsgNotifySaveFailed)
				slog.Error("autoretry: failed to save setting", "project", e.name, "error", err)
			} else {
				if !enabled {
					e.cancelAutoRetry("")
				}
				feedback = e.i18n.T(MsgNotifySaved)
			}
		}
	}
	enabled := e.autoRetryEnabled()
	status, color := MsgAutoRetryOff, "grey"
	if enabled {
		status, color = MsgAutoRetryOn, "green"
	}
	cb := NewCard().Title(e.i18n.T(MsgAutoRetryTitle), color).
		Markdown(e.i18n.Tf(MsgAutoRetryStatus, e.name, e.i18n.T(status)))
	e.autoRetryMu.Lock()
	for _, r := range e.autoRetryRuns {
		if r.userKey != sessionKey || r.ctx.Err() != nil {
			continue
		}
		r.mu.Lock()
		attempts, next := r.attempts, r.next
		r.mu.Unlock()
		cb.Markdown(e.i18n.Tf(MsgAutoRetryCount, attempts))
		if !next.IsZero() {
			cb.Markdown(e.i18n.Tf(MsgAutoRetryNext, next.Format(time.RFC3339)))
		}
	}
	e.autoRetryMu.Unlock()
	if feedback != "" {
		cb.Markdown(feedback)
	}
	prefix := "cmd:"
	if inPlace {
		prefix = "act:"
	}
	on := DefaultBtn(e.i18n.T(MsgAutoRetryOn), prefix+"/autoretry on")
	off := DefaultBtn(e.i18n.T(MsgAutoRetryOff), prefix+"/autoretry off")
	refresh := DefaultBtn(e.i18n.T(MsgAutoRetryRefresh), "cmd:/autoretry")
	if inPlace {
		refresh.Value = "nav:/autoretry"
	}
	if enabled {
		on.Type = "primary"
	} else {
		off.Type = "primary"
	}
	return cb.ButtonsEqual(on, off, refresh).Note(e.i18n.T(MsgAutoRetryUsage)).Build()
}
