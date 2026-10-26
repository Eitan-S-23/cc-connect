package core

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func catalogModelNames(models []ModelOption) []string {
	names := make([]string, len(models))
	for i, model := range models {
		names[i] = model.Name
	}
	return names
}

func TestModelCatalogMergePreservesMetadataAndDeduplicates(t *testing.T) {
	discovered := []ModelOption{{Name: "known", Alias: "alias", Desc: "description"}, {Name: "KNOWN"}, {Name: " "}}
	got := mergeModelCatalog(discovered, []string{"known", "manual/one", "manual/two", "MANUAL/ONE"}, "manual/current")
	if want := []string{"known", "manual/one", "manual/two", "manual/current"}; !reflect.DeepEqual(catalogModelNames(got), want) {
		t.Fatal(got)
	}
	if got[0].Alias != "alias" || got[0].Desc != "description" {
		t.Fatal("discovery metadata lost")
	}
	got[0].Name = "changed"
	if discovered[0].Name != "known" {
		t.Fatal("mutated adapter-owned list")
	}
}

func TestModelCatalogSurvivesDiscoveryRefreshAndProviderIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	agent := &namedStubModelModeAgent{name: "backend", stubModelModeAgent: stubModelModeAgent{
		model: "gpt-4.1", active: "a", providers: []ProviderConfig{{Name: "a", Model: "gpt-4.1"}, {Name: "b", Model: "gpt-4.1"}},
	}}
	e := NewEngine("bot", agent, nil, "", LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(path))
	t.Cleanup(func() { _ = e.Stop() })
	if _, err := e.switchModel("manual/a"); err != nil {
		t.Fatal(err)
	}
	// Simulate a config reload or API refresh that has no manual entries.
	agent.SetProviders([]ProviderConfig{{Name: "a", Model: "gpt-4.1"}, {Name: "b", Model: "gpt-4.1"}})
	agent.SetModel("gpt-4.1")
	want := []string{"gpt-4.1", "gpt-4.1-mini", "manual/a"}
	if got := catalogModelNames(e.availableModels(context.Background(), agent)); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	agent.SetActiveProvider("b")
	if got := catalogModelNames(e.availableModels(context.Background(), agent)); len(got) != 2 {
		t.Fatal("provider leak", got)
	}
	if _, err := e.switchModel("manual/b"); err != nil {
		t.Fatal(err)
	}
	agent.SetActiveProvider("a")
	agent.SetModel("gpt-4.1")
	e.SetProjectStateStore(NewProjectStateStore(path))
	if got := catalogModelNames(e.availableModels(context.Background(), agent)); !reflect.DeepEqual(got, want) {
		t.Fatal("reload lost model or leaked provider", got)
	}
	agent.name = "another-backend"
	if got := catalogModelNames(e.availableModels(context.Background(), agent)); len(got) != 2 {
		t.Fatal("agent leak", got)
	}
	agent.name = "backend"
	other := NewEngine("other-bot", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = other.Stop() })
	other.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "state.json")))
	if got := catalogModelNames(other.availableModels(context.Background(), agent)); len(got) != 2 {
		t.Fatal("project leak", got)
	}
}

func TestModelCatalogSaveFailureDoesNotPublishSelection(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := &stubModelModeAgent{model: "gpt-4.1"}
	e := NewEngine("bot", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(blocker, "state.json")))
	called := false
	e.SetModelSaveFunc(func(string) error { called = true; return nil })
	if _, err := e.switchModel("manual/fail"); err == nil || !strings.Contains(err.Error(), "save model catalog") {
		t.Fatal(err)
	}
	if agent.GetModel() != "gpt-4.1" || called {
		t.Fatal("model changed after failed catalog save")
	}
	if got := e.projectState.rememberedModels(modelCatalogScope(agent)); len(got) != 0 {
		t.Fatal("failed save changed catalog", got)
	}
}

func TestModelCatalogSelectionFailureRollsBackRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	agent := &stubModelModeAgent{model: "gpt-4.1"}
	e := NewEngine("bot", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetProjectStateStore(NewProjectStateStore(path))
	if _, err := e.switchModel("manual/keep"); err != nil {
		t.Fatal(err)
	}
	e.SetModelSaveFunc(func(string) error { return errors.New("config read-only") })
	if _, err := e.switchModel("manual/fail"); err == nil {
		t.Fatal("expected config failure")
	}
	if agent.GetModel() != "manual/keep" {
		t.Fatal("failed selection changed runtime")
	}
	want := []string{"manual/keep"}
	if got := NewProjectStateStore(path).rememberedModels(modelCatalogScope(agent)); !reflect.DeepEqual(got, want) {
		t.Fatal("failed selection persisted catalog entry", got)
	}
}

func TestModelCatalogStoreConcurrencyAndExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewProjectStateStore(path)
	if err := store.SetAutoRetryEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNotifyEnabled(false); err != nil {
		t.Fatal(err)
	}
	store.SetWorkspaceModelOverride("workspace", "selected")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.withRememberedModel("scope", fmt.Sprintf("manual/%d", i), func() error { return nil }); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	models := store.rememberedModels("scope")
	if len(models) != 20 {
		t.Fatal(models)
	}
	models[0] = "mutated"
	if store.rememberedModels("scope")[0] == "mutated" {
		t.Fatal("caller mutated store slice")
	}
	reloaded := NewProjectStateStore(path)
	if len(reloaded.rememberedModels("scope")) != 20 || !reloaded.AutoRetryEnabled() || reloaded.WorkspaceModelOverride("workspace") != "selected" {
		t.Fatal("state lost during model writes")
	}
	if enabled, overridden := reloaded.NotifyEnabled(); enabled || !overridden {
		t.Fatal("notification state lost")
	}
}

func TestModelCatalogCardAndNumberedActionUseSameChoices(t *testing.T) {
	agent := &stubModelModeAgent{model: "gpt-4.1"}
	e := NewEngine("bot", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	if _, err := e.switchModel("manual/card"); err != nil {
		t.Fatal(err)
	}
	card := e.renderModelCard("test:owner")
	var selectElement *CardSelect
	for _, el := range card.Elements {
		if s, ok := el.(CardSelect); ok {
			selectElement = &s
		}
	}
	if selectElement == nil || len(selectElement.Options) != 3 || selectElement.Options[2].Text != "manual/card" || selectElement.InitValue != selectElement.Options[2].Value {
		t.Fatal("manual option is not selected", selectElement)
	}
	if card := e.handleModelCardAction("switch 1", "test:owner"); card.Header.Color != "green" {
		t.Fatal(card.RenderText())
	}
	if card := e.handleModelCardAction("switch 3", "test:owner"); card.Header.Color != "green" || agent.GetModel() != "manual/card" {
		t.Fatal(card.RenderText())
	}
	// The asynchronous card path goes through the same registration helper.
	e.performModelSwitchAsync("test:owner", nil, agent, e.sessions, "manual/async")
	if got := catalogModelNames(e.availableModels(context.Background(), agent)); len(got) != 4 || got[3] != "manual/async" {
		t.Fatal(got)
	}
}

func TestModelCatalogManagementListsRememberedModels(t *testing.T) {
	agent := &stubModelModeAgent{model: "gpt-4.1"}
	e := NewEngine("bot", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	server := &ManagementServer{}
	w := httptest.NewRecorder()
	server.handleProjectModel(w, httptest.NewRequest("POST", "/model", strings.NewReader(`{"model":"manual/web"}`)), e)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	agent.SetModel("gpt-4.1")
	w = httptest.NewRecorder()
	server.handleProjectModels(w, httptest.NewRequest("GET", "/models", nil), e)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"manual/web"`) {
		t.Fatal(w.Body.String())
	}
}

func TestModelCatalogWorkspaceRegistrationKeepsGlobalSelection(t *testing.T) {
	global := &namedStubModelModeAgent{name: "backend", stubModelModeAgent: stubModelModeAgent{model: "global-default"}}
	workspace := &namedStubModelModeAgent{name: "backend", stubModelModeAgent: stubModelModeAgent{model: "workspace-default"}}
	e := NewEngine("bot", global, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	path := filepath.Join(t.TempDir(), "state.json")
	e.SetProjectStateStore(NewProjectStateStore(path))
	e.SetModelSaveFunc(func(string) error { t.Fatal("workspace selection changed global config"); return nil })
	if _, err := e.switchModelOnAgent(workspace, "manual/workspace", false); err != nil {
		t.Fatal(err)
	}
	if global.GetModel() != "global-default" || workspace.GetModel() != "manual/workspace" {
		t.Fatal("wrong selection scope")
	}
	if got := NewProjectStateStore(path).rememberedModels(modelCatalogScope(workspace)); !reflect.DeepEqual(got, []string{"manual/workspace"}) {
		t.Fatal(got)
	}
}
