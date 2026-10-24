package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newNotifyTestEngine(t *testing.T, name string) *Engine {
	t.Helper()
	dir := t.TempDir()
	e := NewEngine(name, nil, nil, filepath.Join(dir, "sessions.json"), LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	e.SetAdminFrom("admin")
	t.Cleanup(e.cancel)
	return e
}

func TestNotifyBotIsolationAndPersistence(t *testing.T) {
	a, b := newNotifyTestEngine(t, "a"), newNotifyTestEngine(t, "b")
	cfg := DefaultAttentionNotifyCfg()
	cfg.Enabled = true
	a.SetAttentionNotify(cfg)
	b.SetAttentionNotify(cfg)
	a.notifyCommandCard([]string{"off"}, "admin", true)
	if a.effectiveAttentionNotify().Enabled || !b.effectiveAttentionNotify().Enabled {
		t.Fatal("disabling bot a must not disable bot b")
	}
	reloaded := NewProjectStateStore(a.projectState.storePath)
	if enabled, overridden := reloaded.NotifyEnabled(); enabled || !overridden {
		t.Fatal("explicit off must survive restart")
	}
	a.SetProjectStateStore(reloaded)
	a.notifyCommandCard([]string{"on"}, "admin", true)
	a.notifyCommandCard([]string{"on"}, "admin", true)
	if enabled, overridden := NewProjectStateStore(reloaded.storePath).NotifyEnabled(); !enabled || !overridden {
		t.Fatal("repeated on must persist on, not toggle")
	}
	if !b.effectiveAttentionNotify().Enabled {
		t.Fatal("enabling a changed bot b")
	}
}

func TestNotifyOnWithDisabledGlobalDefaults(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	if e.effectiveAttentionNotify().Enabled {
		t.Fatal("feature must remain disabled by default")
	}
	card := e.notifyCommandCard([]string{"ON"}, "admin", true)
	cfg := e.effectiveAttentionNotify()
	if !cfg.Enabled || !cfg.OnTurnComplete || !cfg.OnBlocked || !cfg.OnError || !cfg.MentionUser {
		t.Fatalf("on must enable the default triggers: %+v", cfg)
	}
	if !strings.Contains(card.RenderText(), e.i18n.T(MsgNotifySaved)) {
		t.Fatal("on should confirm successful persistence")
	}
}

func TestNotifyPreservesTriggerOptions(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	cfg := AttentionNotifyCfg{OnError: true, MinDuration: time.Minute, Content: "custom"}
	e.SetAttentionNotify(cfg)
	e.notifyCommandCard([]string{"on"}, "admin", true)
	cfg.Enabled = true
	if got := e.effectiveAttentionNotify(); got != cfg {
		t.Fatalf("toggle changed trigger settings: got %+v, want %+v", got, cfg)
	}
	e.notifyCommandCard([]string{"off"}, "admin", true)
	cfg.Enabled = false
	if got := e.effectiveAttentionNotify(); got != cfg {
		t.Fatalf("off changed trigger settings: %+v", got)
	}
}

func TestNotifyStatusAndInvalidArgsDoNotMutate(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	for _, args := range [][]string{nil, {"toggle"}, {"on", "off"}, {"off", "extra"}} {
		card := e.notifyCommandCard(args, "admin", true)
		if card == nil || !strings.Contains(card.RenderText(), "bot") {
			t.Fatalf("missing status card for %v", args)
		}
		if _, overridden := e.projectState.NotifyEnabled(); overridden {
			t.Fatalf("read/invalid command %v wrote an override", args)
		}
	}
}

func TestNotifySaveFailureKeepsPreviousState(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	e.notifyCommandCard([]string{"on"}, "admin", true)
	// A file cannot be used as the parent directory of the state file.
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.projectState.storePath = filepath.Join(parent, "state.json")
	card := e.notifyCommandCard([]string{"off"}, "admin", true)
	if !e.effectiveAttentionNotify().Enabled {
		t.Fatal("failed persistence changed the live state")
	}
	text := card.RenderText()
	if !strings.Contains(text, e.i18n.T(MsgNotifySaveFailed)) || strings.Contains(text, e.i18n.T(MsgNotifySaved)) {
		t.Fatal("save failure should be visible without claiming success")
	}
}

func TestNotifyCardUsesVerifiedActor(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	ownerKey := "feishu:chat:admin"
	card := e.handleAuthenticatedCardNav("act:/notify on", ownerKey, "guest")
	if e.effectiveAttentionNotify().Enabled || !strings.Contains(card.RenderText(), e.i18n.Tf(MsgAdminRequired, "/notify")) {
		t.Fatal("guest must not borrow the card owner's admin identity")
	}
	card = e.handleAuthenticatedCardNav("act:/notify on", "feishu:shared-chat", "admin")
	if !e.effectiveAttentionNotify().Enabled || !strings.Contains(card.RenderText(), e.i18n.T(MsgNotifySaved)) {
		t.Fatal("verified admin must be able to operate a shared-chat card")
	}
	e.handleCardNav("act:/notify off", ownerKey)
	if !e.effectiveAttentionNotify().Enabled {
		t.Fatal("legacy unauthenticated navigation must not change notify")
	}
	e.SetAdminFrom("*")
	e.handleAuthenticatedCardNav("act:/notify off", ownerKey, "")
	if !e.effectiveAttentionNotify().Enabled {
		t.Fatal("missing callback identity must fail closed even with wildcard admin")
	}
}

func TestNotifyDisabledCommandBlocksCardAndText(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	e.SetDisabledCommands([]string{"notify"})
	card := e.handleAuthenticatedCardNav("act:/notify on", "test:admin", "admin")
	if !strings.Contains(card.RenderText(), e.i18n.Tf(MsgCommandDisabled, "/notify")) {
		t.Fatal("disabled command was not rejected by card handler")
	}
	p := &stubPlatformEngine{n: "test"}
	e.handleCommand(p, &Message{UserID: "admin", ReplyCtx: "ctx"}, "/notify on")
	if _, overridden := e.projectState.NotifyEnabled(); overridden {
		t.Fatal("disabled command mutated state")
	}
}

func TestNotifyUserRolePolicyAppliesToCards(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	roles := NewUserRoleManager()
	roles.Configure("restricted", []RoleInput{{
		Name: "restricted", UserIDs: []string{"admin"}, DisabledCommands: []string{"notify"},
	}})
	e.SetUserRoles(roles)
	card := e.handleAuthenticatedCardNav("act:/notify on", "test:admin", "admin")
	if !strings.Contains(card.RenderText(), e.i18n.Tf(MsgCommandDisabled, "/notify")) {
		t.Fatal("card callback bypassed user-role restrictions")
	}
	if _, overridden := e.projectState.NotifyEnabled(); overridden {
		t.Fatal("role-disabled card callback changed the bot state")
	}
}

type notifyNavigablePlatform struct {
	stubCardPlatform
	navigate AuthenticatedCardNavigationHandler
}

func (p *notifyNavigablePlatform) SetAuthenticatedCardNavigationHandler(h AuthenticatedCardNavigationHandler) {
	p.navigate = h
}

func TestNotifyCardCapabilityWiring(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	p := &notifyNavigablePlatform{}
	p.n = "test"
	e.initPlatformCapabilities(p)
	if p.navigate == nil {
		t.Fatal("engine did not wire the authenticated card callback")
	}
	e.ReceiveMessage(p, &Message{Platform: "test", SessionKey: "test:admin", UserID: "admin", Content: "/notify"})
	if len(p.repliedCards) != 1 {
		t.Fatal("/notify did not produce a rich card")
	}
	card := p.navigate("act:/notify on", "test:admin", "admin")
	if !strings.Contains(card.RenderText(), e.i18n.T(MsgNotifySaved)) || !e.effectiveAttentionNotify().Enabled {
		t.Fatal("wired button did not update the bot")
	}
	e.ReceiveMessage(p, &Message{Platform: "test", SessionKey: "test:admin", UserID: "admin", Content: "/notify off"})
	if e.effectiveAttentionNotify().Enabled {
		t.Fatal("text command did not share the card's state")
	}
}

func TestNotifyOffSuppressesAllExtraAlerts(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	cfg := DefaultAttentionNotifyCfg()
	cfg.Enabled = true
	e.SetAttentionNotify(cfg)
	e.notifyCommandCard([]string{"off"}, "admin", true)
	p := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.notifyTurnComplete(p, "ctx", "user", "User", time.Minute, true)
	e.notifyBlocked(p, "ctx", "user", "User", "waiting", true)
	e.notifyAttention(p, "ctx", "user", "User", "failure")
	if len(p.getNotifications()) != 0 || len(p.getSent()) != 0 {
		t.Fatal("off must suppress all extra alerts, including unmentioned notices")
	}
	e.notifyCommandCard([]string{"on"}, "admin", true)
	e.notifyTurnComplete(p, "ctx", "user", "User", time.Minute, true)
	if len(p.getNotifications()) != 1 {
		t.Fatal("on did not immediately restore notifications")
	}
}

func TestNotifyCardButtonActionsAndMenu(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	for _, inPlace := range []bool{false, true} {
		card := e.renderNotifyCard(inPlace, "")
		prefix := "cmd:"
		if inPlace {
			prefix = "act:"
		}
		found := map[string]bool{}
		for _, element := range card.Elements {
			if actions, ok := element.(CardActions); ok {
				for _, button := range actions.Buttons {
					found[button.Value] = true
				}
			}
		}
		if !found[prefix+"/notify on"] || !found[prefix+"/notify off"] {
			t.Fatalf("missing explicit button actions: %v", found)
		}
	}
	for _, command := range e.GetAllCommands() {
		if command.Command == "notify" {
			return
		}
	}
	t.Fatal("notify is missing from the command menu")
}

func TestNotifyConcurrentToggleAndReads(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				if worker%2 == 0 {
					if err := e.projectState.SetNotifyEnabled(i%2 == 0); err != nil {
						t.Errorf("save failed: %v", err)
					}
				} else {
					e.effectiveAttentionNotify()
					e.renderNotifyCard(true, "")
				}
			}
		}()
	}
	wg.Wait()
}

func TestNotifyTranslations(t *testing.T) {
	keys := []MsgKey{MsgNotifyTitle, MsgNotifyStatus, MsgNotifyOn, MsgNotifyOff, MsgNotifyUsage, MsgNotifySaved, MsgNotifySaveFailed, MsgBuiltinCmdNotify}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		if !strings.Contains(NewI18n(lang).T(MsgHelp), "/notify") {
			t.Errorf("plain help is missing notify for %s", lang)
		}
		for _, key := range keys {
			if text := NewI18n(lang).T(key); text == "" || text == string(key) {
				t.Errorf("missing translation %s/%s", lang, key)
			}
		}
	}
}
