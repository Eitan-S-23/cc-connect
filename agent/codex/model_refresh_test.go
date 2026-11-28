package codex

import (
	"context"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRefreshModelsBypassesConfiguredAndNativeCatalogs(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Project") != "project" {
			t.Error("incorrect endpoint/auth")
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-new"},{"id":"gateway/custom-model"}]}`)
	}))
	defer server.Close()
	native := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(native, []byte(`{"models":[{"slug":"gpt-old","visibility":"list","supported_in_api":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{activeIdx: 0, providers: []core.ProviderConfig{{Name: "test", BaseURL: server.URL + "/v1/", APIKey: "key", Models: []core.ModelOption{{Name: "gpt-configured"}}, CodexHTTPHeaders: map[string]string{"X-Project": "project"}}}}
	t.Setenv("CODEX_HOME", filepath.Dir(native))
	if err := os.WriteFile(filepath.Join(filepath.Dir(native), "config.toml"), []byte(`model_catalog_json = "models.json"`), 0600); err != nil {
		t.Fatal(err)
	}
	if models := a.AvailableModels(context.Background()); len(models) != 1 || models[0].Name != "gpt-old" || calls.Load() != 0 {
		t.Fatal("native fixture did not mask provider discovery", models)
	}
	before := a.ModelCatalogKey()
	models, err := a.RefreshModels(context.Background())
	if err != nil || len(models) != 2 || calls.Load() != 1 {
		t.Fatalf("%v %v calls=%d", models, err, calls.Load())
	}
	a.SetProviders([]core.ProviderConfig{{Name: "test", BaseURL: server.URL + "/v1/", APIKey: "different"}})
	if before == a.ModelCatalogKey() {
		t.Fatal("credential change kept stale cache identity")
	}
}
