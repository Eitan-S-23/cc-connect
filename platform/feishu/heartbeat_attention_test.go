package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestHeartbeatAttentionRecipientIsUnambiguous(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	for _, tc := range []struct {
		name string
		ctx  any
		want string
	}{
		{"user_session", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_owner"}, "ou_owner"},
		{"direct_user", replyContext{chatID: "ou_owner", sessionKey: "feishu:ou_owner:ou_owner"}, "ou_owner"},
		{"shared_chat", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat"}, ""},
		{"thread", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:thread:om_root"}, ""},
		{"root", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}, ""},
		{"synthetic_user", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:heartbeat"}, ""},
		{"broadcast", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:all"}, ""},
		{"empty_id", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_"}, ""},
		{"invalid_id", replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user<at>"}, ""},
		{"wrong_chat", replyContext{chatID: "oc_other", sessionKey: "feishu:oc_chat:ou_owner"}, ""},
		{"wrong_platform", replyContext{chatID: "oc_chat", sessionKey: "other:oc_chat:ou_owner"}, ""},
		{"missing_key", replyContext{chatID: "oc_chat"}, ""},
		{"wrong_context", "feishu:oc_chat:ou_owner", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, _, err := p.ResolveAttentionRecipient(tc.ctx)
			if tc.want == "" {
				if err == nil || user != "" {
					t.Fatalf("ambiguous context resolved to %q with error %v", user, err)
				}
			} else if err != nil || user != tc.want {
				t.Fatalf("recipient = %q, %v; want %q", user, err, tc.want)
			}
		})
	}
}

func TestHeartbeatAttentionCreatesRealMentionWithoutOriginalMessage(t *testing.T) {
	type messageRequest struct {
		ReceiveID string `json:"receive_id"`
		MsgType   string `json:"msg_type"`
		Content   string `json:"content"`
	}
	requests := make(chan messageRequest, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "heartbeat-test-token"})
		case "/open-apis/im/v1/messages":
			if r.Method != http.MethodPost || r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Errorf("wrong message operation: %s %s", r.Method, r.URL)
			}
			var req messageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			requests <- req
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_heartbeat"}})
		default:
			t.Errorf("unexpected API operation: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &Platform{
		platformName: "feishu", domain: srv.URL, appID: "heartbeat-test", appSecret: "test-only",
		client: lark.NewClient("heartbeat-test", "test-only", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}
	rctx, err := p.ReconstructReplyCtx("feishu:oc_chat:ou_owner")
	if err != nil {
		t.Fatal(err)
	}
	user, name, err := p.ResolveAttentionRecipient(rctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.NotifyAttention(context.Background(), rctx, user, name, "Task completed."); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("new message count = %d, want 1", len(requests))
	}
	req := <-requests
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.Content), &body); err != nil {
		t.Fatal(err)
	}
	if req.ReceiveID != "oc_chat" || req.MsgType != "text" || !strings.Contains(body.Text, `<at user_id="ou_owner">`) {
		t.Fatalf("heartbeat did not create a real mention in its own chat: %+v; %q", req, body.Text)
	}
	for _, notice := range []string{"Heartbeat needs your input.", "Heartbeat stopped with an error."} {
		if err := p.Send(context.Background(), rctx, notice); err != nil {
			t.Fatal(err)
		}
		if len(requests) != 1 {
			t.Fatalf("plain heartbeat notice created %d messages, want 1", len(requests))
		}
		req := <-requests
		if req.ReceiveID != "oc_chat" || !strings.Contains(req.Content, notice) ||
			strings.Contains(req.Content, "<at") || strings.Contains(req.Content, `\u003cat`) {
			t.Fatalf("plain heartbeat notice was lost, misrouted or mentioned someone: %+v", req)
		}
	}
}
