package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const heartbeatCompleteTestMarker = "[[CC_CONNECT_HEARTBEAT_COMPLETE]]"

func TestHeartbeatAttentionDoneNoticeWithoutMarkerMentionsOwner(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Done: true, Content: "The requested change is ready.",
	}}})
	if err := e.ExecuteHeartbeat("test:owner", "Continue pending work.", true); err != nil {
		t.Fatal(err)
	}
	notices := p.getNotifications()
	if len(notices) != 1 || notices[0].userID != "ou_owner" || notices[0].text != e.i18n.T(MsgAttentionTurnComplete) {
		t.Fatalf("Done notice must mention the heartbeat owner without an extra marker: %+v; plain=%v", notices, p.getSent())
	}
	if heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnComplete)) != 0 {
		t.Fatal("mentioned completion was duplicated as an unmentioned notice")
	}
}

type heartbeatAttentionPlatform struct {
	attentionRecordingPlatform
	recipient    string
	recipientErr error
}

func (p *heartbeatAttentionPlatform) ReconstructReplyCtx(key string) (any, error) {
	return key, nil
}

func (p *heartbeatAttentionPlatform) ResolveAttentionRecipient(any) (string, string, error) {
	return p.recipient, "Owner", p.recipientErr
}

func newHeartbeatAttentionEngine(t *testing.T, agent Agent) (*Engine, *heartbeatAttentionPlatform) {
	t.Helper()
	p := &heartbeatAttentionPlatform{
		attentionRecordingPlatform: attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		recipient:                  "ou_owner",
	}
	e := NewEngine("heartbeat-attention", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.SetAttentionNotify(AttentionNotifyCfg{
		Enabled: true, OnTurnComplete: true, OnBlocked: true, OnError: true, MentionUser: true,
	})
	t.Cleanup(func() { _ = e.Stop() })
	return e, p
}

func TestHeartbeatAttentionCompletionNoticeMentionsOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   Event
		want    int
		visible string
	}{
		{"completed", Event{Type: EventResult, Done: true, Content: "Delivered the fix."}, 1, "Delivered the fix."},
		{"legacy_marker", Event{Type: EventResult, Done: true, Content: "Delivered the fix.\n" + heartbeatCompleteTestMarker}, 1, "Delivered the fix."},
		{"final_reply", Event{Type: EventResult, Done: true, Content: "Here is the requested status."}, 1, "requested status"},
		{"silent", Event{Type: EventResult, Done: true, Content: "NO_REPLY"}, 0, ""},
		{"empty", Event{Type: EventResult, Done: true}, 0, ""},
		{"marker_only", Event{Type: EventResult, Done: true, Content: heartbeatCompleteTestMarker}, 0, ""},
		{"inline_marker", Event{Type: EventResult, Done: true, Content: "Example: " + heartbeatCompleteTestMarker}, 1, "Example:"},
		{"error", Event{Type: EventError, Error: errors.New("provider unavailable")}, 0, "provider unavailable"},
		{"failed_result", Event{Type: EventResult, Done: true, Content: "Failed.\n" + heartbeatCompleteTestMarker, Error: errors.New("build failed")}, 0, "build failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &cujAgent{nextSessionEvents: []Event{tc.event}}
			e, p := newHeartbeatAttentionEngine(t, a)
			if err := e.ExecuteHeartbeat("test:owner", "Continue pending work.", true); err != nil {
				t.Fatal(err)
			}
			got := p.getNotifications()
			if len(got) != tc.want {
				t.Fatalf("notifications = %+v, want %d", got, tc.want)
			}
			if tc.want > 0 && (got[0].userID != "ou_owner" || got[0].replyCtx != "test:owner") {
				t.Fatalf("completion did not target the owner and original chat: %+v", got[0])
			}
			for _, sent := range p.getSent() {
				if strings.Contains(sent, heartbeatCompleteTestMarker) {
					t.Fatalf("internal completion marker leaked: %q", sent)
				}
			}
			if tc.visible != "" && !containsAny(p.getSent(), tc.visible) {
				t.Fatalf("normal reply was lost: %v", p.getSent())
			}
		})
	}
}

func startControlledHeartbeat(t *testing.T, e *Engine, s *attentionControlledSession) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.ExecuteHeartbeat("test:attention", "Continue pending work.", true) }()
	waitAttentionSend(t, s, "")
	return done
}

func waitHeartbeatDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat did not finish")
	}
}

func waitHeartbeatQuestion(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.interactiveMu.Lock()
		state := e.interactiveStates["test:attention"]
		e.interactiveMu.Unlock()
		if state != nil {
			state.mu.Lock()
			pending := state.pending != nil
			state.mu.Unlock()
			if pending {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("question was not shown")
}

func TestHeartbeatAttentionQuestionThenCompletion(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	s.events <- Event{Type: EventPermissionRequest, RequestID: "question", ToolName: "AskUserQuestion", Questions: []UserQuestion{{Question: "Which database?"}}}
	waitHeartbeatQuestion(t, e)
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("heartbeat question generated mentions: %+v", got)
	}
	resolvePendingPermission(t, e, "test:attention")
	s.events <- Event{Type: EventResult, Done: true, Content: "Database configured."}
	waitHeartbeatDone(t, done)
	if got := p.getNotifications(); len(got) != 1 || got[0].userID != "ou_owner" {
		t.Fatalf("expected one completion after the answer: %+v", got)
	}
}

func TestHeartbeatAttentionQueuedUserRetainsErrorMention(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	sendAttentionMessage(e, p, "user-after-heartbeat", "ou_bob")
	s.events <- Event{Type: EventResult, Done: true, Content: "Heartbeat work finished.\n" + heartbeatCompleteTestMarker}
	waitAttentionSend(t, s, "user-after-heartbeat")
	s.events <- Event{Type: EventError, Error: errors.New("user task failed")}
	waitHeartbeatDone(t, done)
	got := p.getNotifications()
	if len(got) != 2 || got[0].userID != "ou_owner" || got[1].userID != "ou_bob" {
		t.Fatalf("heartbeat policy or recipient leaked into queued user turn: %+v", got)
	}
}

func TestHeartbeatAttentionUnknownRecipientDoesNotMention(t *testing.T) {
	a := &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: "Done.\n" + heartbeatCompleteTestMarker}}}
	e, p := newHeartbeatAttentionEngine(t, a)
	p.recipientErr = errors.New("no unambiguous recipient")
	if err := e.ExecuteHeartbeat("test:shared", "Continue.", true); err != nil {
		t.Fatal(err)
	}
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("unknown recipient must not become a broadcast or synthetic-user mention: %+v", got)
	}
	if !containsAny(p.getSent(), "Done.") {
		t.Fatal("missing recipient suppressed the actual reply")
	}
}

func TestHeartbeatAttentionMarkerStreamBoundaries(t *testing.T) {
	input := "Work completed.\n" + heartbeatCompleteTestMarker
	for split := 0; split <= len(input); split++ {
		n := &attentionTurn{heartbeat: &heartbeatAttention{}}
		first := n.filterHeartbeatText(input[:split])
		second := n.filterHeartbeatText(input[split:])
		final, tail := n.finishHeartbeatText("")
		if strings.Contains(first+second, "[[") || strings.TrimSpace(first+second+tail) != "Work completed." {
			t.Fatalf("split %d leaked or lost streaming text: %q / %q / %q", split, first, second, tail)
		}
		if final != "Work completed." || !n.heartbeat.hasReply {
			t.Fatalf("split %d lost the reply: final=%q, hasReply=%v", split, final, n.heartbeat.hasReply)
		}
	}
}

func TestHeartbeatAttentionIncompleteMarkerIsOrdinaryText(t *testing.T) {
	for _, input := range []string{
		"The literal prefix is [[CC",
		heartbeatCompleteTestMarker + "\nThe literal prefix is [[CC",
	} {
		n := &attentionTurn{heartbeat: &heartbeatAttention{}}
		streamed := n.filterHeartbeatText(input)
		final, tail := n.finishHeartbeatText("")
		want := strings.ReplaceAll(input, heartbeatCompleteTestMarker, "")
		if streamed+tail != want || final != want {
			t.Fatalf("partial literal changed: stream=%q tail=%q final=%q", streamed, tail, final)
		}
	}
}

func TestHeartbeatAttentionDoesNotAlterUserText(t *testing.T) {
	n := &attentionTurn{}
	input := "Explain this marker: " + heartbeatCompleteTestMarker
	if n.filterHeartbeatText(input) != input {
		t.Fatal("ordinary user text was changed")
	}
	if final, tail := n.finishHeartbeatText(input); final != input || tail != "" {
		t.Fatal("ordinary user result was changed")
	}
}

func TestHeartbeatAttentionDoesNotInjectCompletionProtocol(t *testing.T) {
	e, _ := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: "Done."}}})
	if err := e.ExecuteHeartbeat("test:owner", "Continue pending work.", true); err != nil {
		t.Fatal(err)
	}
	for _, entry := range e.sessions.GetOrCreateActive("test:owner").GetHistory(0) {
		if entry.Role == "user" {
			if entry.Content != "Continue pending work." {
				t.Fatalf("notification policy modified the heartbeat prompt: %q", entry.Content)
			}
			return
		}
	}
	t.Fatal("heartbeat prompt was not recorded")
}

func TestHeartbeatAttentionStreamedCompletionAndRecovery(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	s.events <- Event{Type: EventText, Content: "Work completed.\n[[CC_CONNECT_"}
	s.events <- Event{Type: EventText, Content: "HEARTBEAT_COMPLETE]]"}
	s.events <- Event{Type: EventResult, Done: true}
	waitHeartbeatDone(t, done)
	if got := p.getNotifications(); len(got) != 1 || got[0].userID != "ou_owner" {
		t.Fatalf("streamed completion lost its recipient: %+v", got)
	}
	for _, sent := range p.getSent() {
		if strings.Contains(sent, "[[CC_CONNECT_") {
			t.Fatalf("stream control marker leaked: %q", sent)
		}
	}
	// A later ordinary user turn keeps its original completion semantics.
	sendAttentionMessage(e, p, "next-user-turn", "ou_alice")
	waitAttentionSend(t, s, "next-user-turn")
	s.events <- Event{Type: EventResult, Done: true, Content: "User task completed."}
	waitAttentionIdle(t, e)
	if got := p.getNotifications(); len(got) != 2 || got[1].userID != "ou_alice" {
		t.Fatalf("heartbeat origin leaked into next user turn: %+v", got)
	}
}

func TestHeartbeatAttentionFailureWhileWaitingStaysQuiet(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	s.events <- Event{Type: EventPermissionRequest, RequestID: "permission", ToolName: "Bash"}
	waitHeartbeatQuestion(t, e)
	s.events <- Event{Type: EventError, Error: errors.New("provider failed while waiting")}
	waitHeartbeatDone(t, done)
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("permission/failure generated heartbeat mentions: %+v", got)
	}
	if !containsAny(p.getSent(), "provider failed while waiting") {
		t.Fatal("failure reply was suppressed")
	}
}

func TestHeartbeatAttentionStartupSendAndExitFailuresStayQuiet(t *testing.T) {
	t.Run("startup", func(t *testing.T) {
		e, p := newHeartbeatAttentionEngine(t, &cujAgent{failStartCount: 1, failStartErr: errors.New("startup failed")})
		if err := e.ExecuteHeartbeat("test:attention", "Continue.", true); err != nil {
			t.Fatal(err)
		}
		if got := p.getNotifications(); len(got) != 0 {
			t.Fatalf("startup failure generated mentions: %+v", got)
		}
		if heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionStartFailed)) != 1 {
			t.Fatalf("startup failure lost its ordinary notice: %v", p.getSent())
		}
	})
	for _, name := range []string{"send", "exit", "idle_timeout"} {
		t.Run(name, func(t *testing.T) {
			s := newAttentionControlledSession()
			if name == "send" {
				s.sendError = errors.New("send failed")
			}
			e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
			if name == "idle_timeout" {
				e.eventIdleTimeout = 30 * time.Millisecond
			}
			done := startControlledHeartbeat(t, e, s)
			if name == "exit" {
				_ = s.Close()
			}
			waitHeartbeatDone(t, done)
			if got := p.getNotifications(); len(got) != 0 {
				t.Fatalf("%s failure generated mentions: %+v", name, got)
			}
			want := MsgAttentionSendFailed
			if name == "exit" {
				want = MsgAttentionProcessExited
			} else if name == "idle_timeout" {
				want = MsgAttentionIdleTimeout
			}
			if heartbeatPlainNoticeCount(p, e.i18n.T(want)) != 1 {
				t.Fatalf("%s failure lost its ordinary notice: %v", name, p.getSent())
			}
		})
	}
}

func TestHeartbeatAttentionRespectsExistingConfig(t *testing.T) {
	for _, name := range []string{"disabled", "completion_disabled", "minimum_duration", "mention_disabled"} {
		t.Run(name, func(t *testing.T) {
			a := &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: "Done.\n" + heartbeatCompleteTestMarker}}}
			e, p := newHeartbeatAttentionEngine(t, a)
			switch name {
			case "disabled":
				e.attentionNotify.Enabled = false
			case "completion_disabled":
				e.attentionNotify.OnTurnComplete = false
			case "minimum_duration":
				e.attentionNotify.MinDuration = time.Hour
			case "mention_disabled":
				e.attentionNotify.MentionUser = false
			}
			if err := e.ExecuteHeartbeat("test:owner", "Continue.", true); err != nil {
				t.Fatal(err)
			}
			if got := p.getNotifications(); len(got) != 0 {
				t.Fatalf("%s config generated a mention: %+v", name, got)
			}
			wantPlain := 0
			if name == "mention_disabled" {
				wantPlain = 1
			}
			if got := heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnComplete)); got != wantPlain {
				t.Fatalf("%s config generated %d plain notices, want %d", name, got, wantPlain)
			}
		})
	}
}

func TestHeartbeatAttentionBackgroundCompletionAndError(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	s.events <- Event{Type: EventResult, Done: true, Content: "NO_REPLY"}
	waitHeartbeatDone(t, done)

	// Work that finishes on the same agent's unsolicited event stream retains
	// the heartbeat policy, including suppression of a later terminal error.
	s.events <- Event{Type: EventText, Content: "Background task completed."}
	s.events <- Event{Type: EventResult, Done: true}
	got := waitForAttention(t, &p.attentionRecordingPlatform, 1)
	if len(got) != 1 || got[0].userID != "ou_owner" {
		t.Fatalf("background completion lost its heartbeat routing: %+v", got)
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:attention"]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	readerDone := state.unsolicitedDone
	state.mu.Unlock()
	s.events <- Event{Type: EventError, Error: errors.New("late background failure")}
	select {
	case <-readerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("background error did not stop the event reader")
	}
	if got := p.getNotifications(); len(got) != 1 {
		t.Fatalf("background failure generated another mention: %+v", got)
	}
}

func heartbeatPlainNoticeCount(p *heartbeatAttentionPlatform, text string) int {
	count := 0
	for _, sent := range p.getSent() {
		if sent == text {
			count++
		}
	}
	return count
}

func waitHeartbeatPlainNotice(t *testing.T, p *heartbeatAttentionPlatform, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && heartbeatPlainNoticeCount(p, text) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if count := heartbeatPlainNoticeCount(p, text); count != 1 {
		t.Fatalf("plain notice count = %d, want 1 for %q; replies=%v", count, text, p.getSent())
	}
}

func TestHeartbeatAttentionUnmarkedDoneNoticeMentionsOwner(t *testing.T) {
	for _, reply := range []string{
		"This is a new conversation. Please provide the task or open the original conversation.",
		"Delivered the fix without an internal marker.",
		"Which database should I use?",
		"The build is still running.",
		"There are no pending tasks.",
	} {
		t.Run(reply, func(t *testing.T) {
			e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: reply}}})
			e.attentionNotify.Content = "Custom completion notice."
			if err := e.ExecuteHeartbeat("test:owner", "Continue pending work.", true); err != nil {
				t.Fatal(err)
			}
			if got := heartbeatPlainNoticeCount(p, e.attentionNotify.Content); got != 0 {
				t.Fatalf("completion was sent without a mention: %v", p.getSent())
			}
			if got := p.getNotifications(); len(got) != 1 || got[0].userID != "ou_owner" || got[0].text != e.attentionNotify.Content {
				t.Fatalf("unmarked completion notice lost its mention: %+v", got)
			}
			if !containsAny(p.getSent(), reply) {
				t.Fatal("the normal reply was replaced by the notice")
			}
		})
	}
}

func TestHeartbeatAttentionEmptyAndSilentRepliesDoNotAddPlainNotice(t *testing.T) {
	for _, reply := range []string{"", "NO_REPLY", heartbeatCompleteTestMarker, "NO_REPLY\n" + heartbeatCompleteTestMarker} {
		t.Run(reply, func(t *testing.T) {
			e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: reply}}})
			e.attentionNotify.Content = "Plain completion notice."
			if err := e.ExecuteHeartbeat("test:owner", "Continue.", true); err != nil {
				t.Fatal(err)
			}
			if heartbeatPlainNoticeCount(p, e.attentionNotify.Content) != 0 || len(p.getNotifications()) != 0 {
				t.Fatalf("empty/silent reply generated a notice: %v; %+v", p.getSent(), p.getNotifications())
			}
		})
	}
}

func TestHeartbeatAttentionMissingRecipientKeepsPlainNotice(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Done: true, Content: "Delivered.\n" + heartbeatCompleteTestMarker,
	}}})
	p.recipientErr = errors.New("ambiguous shared chat")
	if err := e.ExecuteHeartbeat("test:shared", "Continue.", true); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnComplete)); got != 1 {
		t.Fatalf("missing recipient swallowed the completion notice: %v", p.getSent())
	}
	if len(p.getNotifications()) != 0 {
		t.Fatal("ambiguous recipient generated a mention")
	}
}

func TestHeartbeatAttentionErrorsKeepPlainNotices(t *testing.T) {
	for _, event := range []Event{
		{Type: EventError, Error: errors.New("provider unavailable")},
		{Type: EventError},
		{Type: EventResult, Done: true, Content: "Failed.\n" + heartbeatCompleteTestMarker, Error: errors.New("task failed")},
	} {
		e, p := newHeartbeatAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{event}})
		if err := e.ExecuteHeartbeat("test:owner", "Continue.", true); err != nil {
			t.Fatal(err)
		}
		if got := heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnFailed)); got != 1 {
			t.Fatalf("error notice count = %d, want 1; replies=%v", got, p.getSent())
		}
		if len(p.getNotifications()) != 0 || heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnComplete)) != 0 {
			t.Fatal("heartbeat failure generated a mention or a success notice")
		}
	}
}

func TestHeartbeatAttentionBlockedNoticesArePlainAndDeduplicated(t *testing.T) {
	e, p := newHeartbeatAttentionEngine(t, &cujAgent{})
	n := e.newAttentionTurn(&interactiveState{platform: p, fromHeartbeat: true, currentUserID: "heartbeat"}, "test:owner", time.Now())
	n.notifyBlocked("permission", "Permission requested.")
	n.notifyBlocked("permission", "Permission requested.")
	n.notifyBlocked("question", "Question requested.")
	if heartbeatPlainNoticeCount(p, "Permission requested.") != 1 || heartbeatPlainNoticeCount(p, "Question requested.") != 1 {
		t.Fatalf("blocked notices were dropped or duplicated: %v", p.getSent())
	}
	if len(p.getNotifications()) != 0 {
		t.Fatal("heartbeat blocked notices mentioned someone")
	}
}

func TestHeartbeatAttentionUnmarkedDonePreservesQueuedUserMention(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newHeartbeatAttentionEngine(t, &attentionControlledAgent{session: s})
	done := startControlledHeartbeat(t, e, s)
	sendAttentionMessage(e, p, "manual-after-unmarked-heartbeat", "ou_bob")
	s.events <- Event{Type: EventResult, Done: true, Content: "The heartbeat round is complete."}
	waitAttentionSend(t, s, "manual-after-unmarked-heartbeat")
	s.events <- Event{Type: EventError, Error: errors.New("manual task failed")}
	waitHeartbeatDone(t, done)
	if heartbeatPlainNoticeCount(p, e.i18n.T(MsgAttentionTurnComplete)) != 0 {
		t.Fatalf("heartbeat completion was sent unmentioned: %v", p.getSent())
	}
	if got := p.getNotifications(); len(got) != 2 || got[0].userID != "ou_owner" || got[1].userID != "ou_bob" {
		t.Fatalf("heartbeat mention policy leaked into the queued user turn: %+v", got)
	}
}
