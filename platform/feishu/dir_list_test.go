package feishu

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestDirListCardCallback_VerifiedActorAndAllowFrom(t *testing.T) {
	for _, action := range []struct{ name, value string }{
		{"home", "nav:/dir ls L3dvcmtzcGFjZS9kZW1v 1"},
		{"previous", "nav:/dir ls L3dvcmtzcGFjZS9kZW1v 2"},
		{"next", "nav:/dir ls L3dvcmtzcGFjZS9kZW1v 3"},
		{"select", "act:/dir ls-select L3dvcmtzcGFjZS9kZW1v L3dvcmtzcGFjZS9kZW1vL2NoaWxk 1"},
	} {
		for _, tc := range []struct {
			name, actor     string
			allowFrom       string
			allowChat       string
			missingOperator bool
			allowed         bool
		}{
			{name: "allowed", actor: "ou_allowed", allowed: true},
			{name: "blocked", actor: "ou_guest"},
			{name: "empty"},
			{name: "missing", missingOperator: true},
			{name: "wildcard-allowed", actor: "ou_allowed", allowFrom: "*", allowed: true},
			{name: "wildcard-empty", allowFrom: "*"},
			{name: "wildcard-missing", allowFrom: "*", missingOperator: true},
			{name: "wrong-chat", actor: "ou_allowed", allowChat: "other"},
		} {
			t.Run(action.name+"/"+tc.name, func(t *testing.T) {
				allowFrom := tc.allowFrom
				if allowFrom == "" {
					allowFrom = "ou_allowed"
				}
				p, err := New(map[string]any{
					"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true,
					"allow_from": allowFrom, "allow_chat": tc.allowChat,
				})
				if err != nil {
					t.Fatal(err)
				}
				ip := p.(*interactivePlatform)
				calls := 0
				ip.SetAuthenticatedCardNavigationHandler(func(value, sessionKey, userID string) *core.Card {
					calls++
					if value != action.value || sessionKey != "feishu:chat:owner" || userID != "ou_allowed" {
						t.Errorf("incorrect callback identity: %q %q %q", value, sessionKey, userID)
					}
					return core.NewCard().Title("Folders", "turquoise").Markdown("Page 1/1").Build()
				})
				var operator *callback.Operator
				if !tc.missingOperator {
					operator = &callback.Operator{OpenID: tc.actor}
				}
				resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
					Operator: operator,
					Action: &callback.CallBackAction{Value: map[string]any{
						"action": action.value, "session_key": "feishu:chat:owner",
					}},
					Context: &callback.Context{OpenChatID: "chat", OpenMessageID: "original"},
				}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.allowed {
					if calls != 1 || resp == nil || resp.Card == nil || resp.Card.Type != "raw" {
						t.Fatalf("missing in-place update: calls=%d response=%#v", calls, resp)
					}
				} else if calls != 0 || resp != nil {
					t.Fatalf("disallowed operator reached handler: calls=%d response=%#v", calls, resp)
				}
			})
		}
	}
}
