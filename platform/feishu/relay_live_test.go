package feishu

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestPrivateChatDetectionRejectsUnknownAndGroupContexts(t *testing.T) {
	p := &Platform{}
	for _, tc := range []struct {
		ctx  any
		want bool
	}{
		{replyContext{privateChat: true}, true},
		{replyContext{}, false},
		{"feishu:chat:user", false},
		{nil, false},
	} {
		if got := p.IsPrivateChat(tc.ctx); got != tc.want {
			t.Fatalf("private=%v want=%v", got, tc.want)
		}
	}
}

func TestRelayCardCallbackAuthenticatesClicker(t *testing.T) {
	for _, actor := range []string{"ou_admin", "ou_guest", ""} {
		p, err := New(map[string]any{"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true, "allow_from": "ou_admin"})
		if err != nil {
			t.Fatal(err)
		}
		ip := p.(*interactivePlatform)
		calls := 0
		ip.SetAuthenticatedCardNavigationHandler(func(action, key, user string) *core.Card {
			calls++
			if action != "act:/relay on" || user != "ou_admin" || key != "feishu:chat:owner" {
				t.Fatalf("wrong callback identity %q %q %q", action, key, user)
			}
			return core.NewCard().Title("Relay", "green").Markdown("On").Build()
		})
		resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: actor},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/relay on", "session_key": "feishu:chat:owner"}},
			Context:  &callback.Context{OpenChatID: "chat", OpenMessageID: "card"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if actor == "ou_admin" {
			if calls != 1 || resp == nil || resp.Card == nil {
				t.Fatal("authorized callback failed")
			}
		} else if calls != 0 || resp != nil {
			t.Fatal("unauthorized callback accepted")
		}
	}
}
