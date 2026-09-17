package discovery

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorouter/gorouter/internal/store"
)

type Service struct {
	Store  *store.Store
	Client *http.Client
}
type Result struct {
	NodeID string
	Models int
	URL    string
}

func (s Service) RefreshNode(ctx context.Context, nodeID string) (Result, error) {
	node, err := s.Store.ProviderNode(nodeID)
	if err != nil {
		return Result{}, err
	}
	if node.ID == "" {
		return Result{}, fmt.Errorf("provider node %q not found", nodeID)
	}
	base := strings.TrimRight(node.BaseURL, "/")
	if base == "" {
		return Result{}, fmt.Errorf("provider node %q has no base URL", nodeID)
	}
	path := node.ModelsPath
	if path == "" {
		path = "/models"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	if credential, ok := s.Store.ConnectionCredential(nodeID); ok && credential.Secret != "" && (credential.Type == "apikey" || credential.Type == "api_key") {
		req.Header.Set("Authorization", "Bearer "+credential.Secret)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return Result{}, fmt.Errorf("models endpoint returned %s", response.Status)
	}
	var payload any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Result{}, err
	}
	ids := modelIDs(payload)
	for _, id := range ids {
		routeID := "route_" + hash(nodeID+"\x00"+id)
		if err := s.Store.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: routeID, ProviderNodeID: nodeID, Kind: "discovered", ExternalID: id, DisplayName: id, Raw: map[string]any{"source": "models_endpoint"}}); err != nil {
			return Result{}, err
		}
	}
	return Result{NodeID: nodeID, Models: len(ids), URL: url}, nil
}

func modelIDs(payload any) []string {
	var raw []any
	switch value := payload.(type) {
	case []any:
		raw = value
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if candidate, ok := value[key].([]any); ok {
				raw = candidate
				break
			}
		}
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		id := ""
		if object, ok := item.(map[string]any); ok {
			for _, key := range []string{"id", "name", "model"} {
				if value, ok := object[key].(string); ok && value != "" {
					id = value
					break
				}
			}
		} else if value, ok := item.(string); ok {
			id = value
		}
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func hash(value string) string { sum := sha1.Sum([]byte(value)); return hex.EncodeToString(sum[:8]) }
