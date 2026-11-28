package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchModelCatalogNormalizesURLAndAuthenticates(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/", "/proxy/v1/"} {
		t.Run(suffix, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := "/v1/models"
				if strings.Contains(suffix, "proxy") {
					want = "/proxy/v1/models"
				}
				if r.URL.Path != want || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Errorf("incorrect endpoint/auth")
				}
				fmt.Fprint(w, `{"data":[{"id":"z"},{"id":"a","display_name":"Alpha"},{"id":"z"},{"id":""}]}`)
			}))
			defer server.Close()
			models, err := FetchModelCatalog(context.Background(), server.URL+suffix, map[string]string{"Authorization": "Bearer test-secret"})
			if err != nil || !reflect.DeepEqual(catalogModelNames(models), []string{"a", "z"}) || models[0].Desc != "Alpha" {
				t.Fatalf("%+v %v", models, err)
			}
		})
	}
}

func TestFetchModelCatalogFailureNeverExposesSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"auth", "test-secret", 401}, {"missing", "test-secret", 404}, {"invalid", `not-json-test-secret`, 200},
		{"empty", `{"data":[]}`, 200}, {"large", strings.Repeat("x", (4<<20)+1), 200},
		{"broken-pagination", `{"data":[{"id":"a"}],"has_more":true}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			models, err := FetchModelCatalog(context.Background(), server.URL+"?token=test-secret", nil)
			if err == nil || len(models) > 0 || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unsafe/incorrect error: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchModelCatalog(ctx, "http://127.0.0.1:1", nil); err == nil {
		t.Fatal("cancelled refresh succeeded")
	}
}

func TestFetchModelCatalogPaginationAndNoCredentialRedirect(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("after_id") == "a" {
			fmt.Fprint(w, `{"data":[{"id":"b"}]}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"a"}],"last_id":"a","has_more":true}`)
		}
	}))
	defer server.Close()
	models, err := FetchModelCatalog(context.Background(), server.URL, nil)
	if err != nil || len(models) != 2 || calls != 2 {
		t.Fatalf("%v %v calls=%d", models, err, calls)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed authenticated redirect") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	if _, err := FetchModelCatalog(context.Background(), redirect.URL, map[string]string{"x-api-key": "secret"}); err == nil {
		t.Fatal("redirect accepted")
	}
}

type refreshTestAgent struct {
	stubModelModeAgent
	key   string
	fetch func(context.Context) ([]ModelOption, error)
	calls atomic.Int32
}

func (a *refreshTestAgent) ModelCatalogKey() string { return a.key }
func (a *refreshTestAgent) RefreshModels(ctx context.Context) ([]ModelOption, error) {
	a.calls.Add(1)
	return a.fetch(ctx)
}

func newRefreshTestEngine(t *testing.T) (*Engine, *stubPlatformEngine, *refreshTestAgent) {
	t.Helper()
	a := &refreshTestAgent{stubModelModeAgent: stubModelModeAgent{model: "gpt-4.1"}, key: "provider-a"}
	a.fetch = func(context.Context) ([]ModelOption, error) { return []ModelOption{{Name: "new-model"}}, nil }
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("refresh", a, []Platform{p}, "", LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "state.json")))
	t.Cleanup(func() { _ = e.Stop() })
	return e, p, a
}

func TestModelRefreshPersistsAndPreservesSessionAndManualModels(t *testing.T) {
	e, p, a := newRefreshTestEngine(t)
	key := "plain:owner"
	s := e.sessions.GetOrCreateActive(key)
	s.SetAgentSessionID("existing", "test")
	s.AddHistory("user", "keep me")
	gen, ok := s.TryLock()
	if !ok {
		t.Fatal("lock failed")
	}
	defer s.Unlock(gen)
	as := newControllableSession("running")
	e.interactiveStates[key] = &interactiveState{agentSession: as, busySession: s, platform: p, replyCtx: "ctx"}
	if err := e.projectState.withRememberedModel(modelCatalogScope(a), "manual-model", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	msg := &Message{SessionKey: key, ReplyCtx: "ctx"}
	e.cmdModel(p, msg, []string{"refresh"})
	if a.GetModel() != "gpt-4.1" || isClosed(as.closed) || !s.Busy() || s.GetAgentSessionID() != "existing" || len(s.GetHistory(0)) != 1 {
		t.Fatal("refresh interrupted the session")
	}
	want := []string{"new-model", "manual-model", "gpt-4.1"}
	e.SetProjectStateStore(NewProjectStateStore(e.projectState.storePath))
	if got := catalogModelNames(e.availableModels(context.Background(), a)); !reflect.DeepEqual(got, want) {
		t.Fatal("catalog lost on restart", got)
	}
	oldCard := e.renderModelCard(key)
	if countCardActionValues(oldCard, "cmd:/model refresh") != 1 {
		t.Fatal("missing asynchronous refresh button")
	}
	a.fetch = func(context.Context) ([]ModelOption, error) { return nil, errors.New("secret") }
	e.cmdModel(p, msg, []string{"refresh"})
	if got := catalogModelNames(e.availableModels(context.Background(), a)); !reflect.DeepEqual(got, want) {
		t.Fatal("failed refresh erased catalog")
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "previous list was kept") || strings.Contains(strings.Join(p.getSent(), "\n"), "secret") {
		t.Fatal("incorrect failure notice")
	}
	a.key = "provider-b"
	if got := catalogModelNames(e.availableModels(context.Background(), a)); strings.Contains(strings.Join(got, ","), "new-model") {
		t.Fatal("provider catalog leaked")
	}
}

func TestModelRefreshConcurrentAndProviderChangeDiscard(t *testing.T) {
	e, p, a := newRefreshTestEngine(t)
	started, release := make(chan struct{}), make(chan struct{})
	a.fetch = func(context.Context) ([]ModelOption, error) {
		close(started)
		<-release
		return []ModelOption{{Name: "wrong-provider"}}, nil
	}
	msg := &Message{SessionKey: "plain:owner", ReplyCtx: "ctx"}
	done := make(chan struct{})
	go func() { e.cmdModel(p, msg, []string{"refresh"}); close(done) }()
	<-started
	e.cmdModel(p, msg, []string{"refresh"})
	if a.calls.Load() != 1 || !strings.Contains(strings.Join(p.getSent(), "\n"), "already in progress") {
		t.Fatal("duplicate refresh was not rejected")
	}
	a.key = "provider-b" // synchronized with the request's completion through release
	close(release)
	<-done
	if len(e.projectState.state.RefreshedModels) != 0 || !strings.Contains(strings.Join(p.getSent(), "\n"), "provider changed") {
		t.Fatal("stale provider result was published")
	}
}

func TestModelRefreshStoreFailureRollsBack(t *testing.T) {
	e, p, a := newRefreshTestEngine(t)
	scope := modelRefreshScope(a, a)
	if err := e.projectState.saveRefreshedModels(scope, []ModelOption{{Name: "keep"}}); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	e.projectState.storePath = filepath.Join(blocker, "state.json")
	e.cmdModel(p, &Message{SessionKey: "plain:owner"}, []string{"refresh"})
	if got := e.projectState.refreshedModels(scope).Models; len(got) != 1 || got[0].Name != "keep" {
		t.Fatal("failed save was published", got)
	}
}

func TestModelCardSelectionSurvivesListReordering(t *testing.T) {
	e, p, a := newRefreshTestEngine(t)
	msg := &Message{SessionKey: "plain:owner"}
	e.cmdModel(p, msg, []string{"refresh"})
	var action string
	for _, el := range e.renderModelCard(msg.SessionKey).Elements {
		if sel, ok := el.(CardSelect); ok {
			for _, o := range sel.Options {
				if o.Text == "new-model" {
					action = o.Value
				}
			}
		}
	}
	a.fetch = func(context.Context) ([]ModelOption, error) {
		return []ModelOption{{Name: "a-new-first"}, {Name: "new-model"}}, nil
	}
	e.cmdModel(p, msg, []string{"refresh"})
	card := e.handleModelCardAction(strings.TrimPrefix(action, "act:/model "), msg.SessionKey)
	if a.GetModel() != "new-model" || card.Header.Color != "green" {
		t.Fatal("old card selected the wrong model", action, card.RenderText())
	}
}

func TestModelRefreshTimeoutIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := FetchModelCatalog(ctx, server.URL, nil)
	var refreshErr *ModelRefreshError
	if !errors.As(err, &refreshErr) || refreshErr.Reason != "timeout" {
		t.Fatal(err)
	}
}
