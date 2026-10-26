package feishu

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestNotifyCardCallbackPassesActorAndUpdatesOriginal(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true})
	if err != nil {
		t.Fatal(err)
	}
	ip := p.(*interactivePlatform)
	ip.SetAuthenticatedCardNavigationHandler(func(action, sessionKey, userID string) *core.Card {
		if action != "act:/notify off" || sessionKey != "feishu:chat:owner" || userID != "ou_clicker" {
			t.Errorf("incorrect callback identity: %q %q %q", action, sessionKey, userID)
		}
		return core.NewCard().Title("Attention Alerts", "grey").Markdown("Off").Build()
	})
	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_clicker"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action": "act:/notify off", "session_key": "feishu:chat:owner",
			}},
			Context: &callback.Context{OpenChatID: "chat", OpenMessageID: "original-card"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Card == nil || resp.Card.Type != "raw" {
		t.Fatalf("callback did not return an in-place card update: %#v", resp)
	}
}

func TestNotifyCardCallbackHonorsAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true,
		"allow_from": "ou_allowed",
	})
	if err != nil {
		t.Fatal(err)
	}
	ip := p.(*interactivePlatform)
	ip.SetAuthenticatedCardNavigationHandler(func(action, sessionKey, userID string) *core.Card {
		t.Error("disallowed card actor reached the command handler")
		return nil
	})
	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_guest"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action": "act:/notify on", "session_key": "feishu:chat:ou_allowed",
			}},
			Context: &callback.Context{OpenChatID: "chat", OpenMessageID: "original-card"},
		},
	})
	if err != nil || resp != nil {
		t.Fatalf("disallowed callback should be ignored: %#v, %v", resp, err)
	}
}

func TestAutoRetryCardCallbackActorAndAllowList(t *testing.T) {
	for _, actor := range []string{"ou_allowed", "ou_guest", ""} {
		p, err := New(map[string]any{"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true, "allow_from": "ou_allowed"})
		if err != nil {
			t.Fatal(err)
		}
		ip := p.(*interactivePlatform)
		calls := 0
		ip.SetAuthenticatedCardNavigationHandler(func(action, key, user string) *core.Card {
			calls++
			if action != "act:/autoretry on" || key != "feishu:chat:owner" || user != "ou_allowed" {
				t.Fatalf("bad routing %q %q %q", action, key, user)
			}
			return core.NewCard().Title("Auto Retry", "green").Markdown("On").Build()
		})
		resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: actor},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/autoretry on", "session_key": "feishu:chat:owner"}},
			Context:  &callback.Context{OpenChatID: "chat", OpenMessageID: "original"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if actor == "ou_allowed" {
			if calls != 1 || resp == nil || resp.Card == nil {
				t.Fatal("missing in-place update")
			}
		} else if calls != 0 || resp != nil {
			t.Fatal("unverified actor reached handler")
		}
	}
}
