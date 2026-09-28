package core

import "strings"

const heartbeatCompletionMarker = "[[CC_CONNECT_HEARTBEAT_COMPLETE]]"

// Strip markers left in older conversation histories, but never require a
// model-authored marker to mention an ordinary turn-completion notification.
type heartbeatAttention struct {
	rawText  strings.Builder
	pending  string
	hasReply bool
}

func (n *attentionTurn) filterHeartbeatText(text string) string {
	if n == nil || n.heartbeat == nil {
		return text
	}
	h := n.heartbeat
	h.rawText.WriteString(text)
	text = strings.ReplaceAll(h.pending+text, heartbeatCompletionMarker, "")
	h.pending = ""
	// Hold a possible split marker so no preview/card can display its prefix.
	for size := min(len(text), len(heartbeatCompletionMarker)-1); size > 0; size-- {
		if strings.HasSuffix(text, heartbeatCompletionMarker[:size]) {
			h.pending = text[len(text)-size:]
			return text[:len(text)-size]
		}
	}
	return text
}

func (n *attentionTurn) finishHeartbeatText(text string) (string, string) {
	if n == nil || n.heartbeat == nil {
		return text, ""
	}
	h := n.heartbeat
	if text == "" {
		text = h.rawText.String()
	}
	trimmed := strings.TrimSpace(text)
	body, marked := strings.CutSuffix(trimmed, heartbeatCompletionMarker)
	body = strings.TrimSpace(body)
	_, silentSuffix := stripTrailingSilent(body)
	h.hasReply = body != "" && !isSilentReply(body) && !silentSuffix
	tail := h.pending
	h.pending = ""
	if marked {
		// A final result can complete a marker that was only partially streamed.
		tail = ""
	}
	if strings.Contains(text, heartbeatCompletionMarker) {
		text = strings.TrimRight(strings.ReplaceAll(text, heartbeatCompletionMarker, ""), " \t\r\n")
	}
	return text, tail
}
