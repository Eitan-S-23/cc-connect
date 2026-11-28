package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ModelRefreshError deliberately excludes response bodies, URLs and credentials.
type ModelRefreshError struct {
	Reason string
	Status int
}

func (e *ModelRefreshError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("model catalog HTTP %d", e.Status)
	}
	return "model catalog: " + e.Reason
}

// ModelCatalogKey identifies effective discovery settings without storing secrets.
func ModelCatalogKey(baseURL string, headers map[string]string) string {
	data, _ := json.Marshal([]any{strings.TrimRight(baseURL, "/"), headers})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FetchModelCatalog queries a /v1/models-compatible endpoint. Explicit refresh
// never falls back to configured or built-in models on failure.
func FetchModelCatalog(ctx context.Context, baseURL string, headers map[string]string) ([]ModelOption, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, &ModelRefreshError{Reason: "invalid_endpoint"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.Path += "/models"
	u.RawPath, u.Fragment = "", ""
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var models []ModelOption
	seen := make(map[string]bool)
	lastID := ""
	for page := 0; page < 100; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, &ModelRefreshError{Reason: "invalid_endpoint"}
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, &ModelRefreshError{Reason: "timeout"}
			}
			return nil, &ModelRefreshError{Reason: "network"}
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, &ModelRefreshError{Reason: "http", Status: resp.StatusCode}
		}
		var result struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if readErr != nil || len(data) > 4<<20 || json.Unmarshal(data, &result) != nil {
			return nil, &ModelRefreshError{Reason: "invalid_response"}
		}
		for _, model := range result.Data {
			name := strings.TrimSpace(model.ID)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			models = append(models, ModelOption{Name: name, Desc: model.DisplayName})
		}
		if !result.HasMore {
			if len(models) == 0 {
				return nil, &ModelRefreshError{Reason: "empty"}
			}
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			return models, nil
		}
		if result.LastID == "" || result.LastID == lastID {
			return nil, &ModelRefreshError{Reason: "invalid_response"}
		}
		lastID = result.LastID
		query := u.Query()
		query.Set("after_id", lastID)
		u.RawQuery = query.Encode()
	}
	return nil, &ModelRefreshError{Reason: "invalid_response"}
}
