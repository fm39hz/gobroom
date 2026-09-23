package discovery

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
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
	if node.BaseURL == "" {
		return Result{}, fmt.Errorf("provider node %q has no base URL", nodeID)
	}
	url := node.BaseURL + node.ModelsPath
	credential, _ := s.Store.ConnectionCredential(nodeID)
	models, err := (provider.OpenAIModelSource{}).List(ctx, provider.ModelSourceInput{BaseURL: node.BaseURL, ModelsPath: node.ModelsPath, Credential: kernel.Credential{Type: credential.Type, Secret: credential.Secret}, Client: s.Client})
	if err != nil {
		return Result{}, err
	}
	for _, model := range models {
		id := model.ID
		routeID := "route_" + hash(nodeID+"\x00"+id)
		raw := model.Metadata
		if raw == nil {
			raw = map[string]any{}
		}
		raw["source"] = "models_endpoint"
		if err := s.Store.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: routeID, ProviderNodeID: nodeID, Kind: "discovered", ExternalID: id, DisplayName: model.DisplayName, Profile: model.Profile, Raw: raw}); err != nil {
			return Result{}, err
		}
	}
	return Result{NodeID: nodeID, Models: len(models), URL: url}, nil
}

func hash(value string) string { sum := sha1.Sum([]byte(value)); return hex.EncodeToString(sum[:8]) }
