package claudecode

import (
	"context"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshModelsBypassesConfiguredCatalogAndUsesAnthropicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("incorrect discovery request")
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-new","display_name":"New"}]}`)
	}))
	defer srv.Close()
	a := &Agent{activeIdx: 0, providers: []core.ProviderConfig{{Name: "test", BaseURL: srv.URL + "/v1", APIKey: "test-key", Models: []core.ModelOption{{Name: "old"}}}}}
	models, err := a.RefreshModels(context.Background())
	if err != nil || len(models) != 1 || models[0].Name != "claude-new" || models[0].Desc != "New" {
		t.Fatalf("%+v %v", models, err)
	}
}
