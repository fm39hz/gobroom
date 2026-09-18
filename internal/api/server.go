package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	store      *store.Store
	control    *controlplane.Manager
	started    time.Time
	executor   func(context.Context, normalize.Request, http.ResponseWriter) error
	reloadHook func() error
}

type HandlerOptions struct {
	ControlPlane bool
	DataPlane    bool
}

func NewServer(s *store.Store) *Server {
	manager, err := controlplane.NewManager(s)
	if err != nil {
		manager = nil
	}
	return &Server{store: s, control: manager, started: time.Now()}
}

func (s *Server) Handler() http.Handler {
	return s.HandlerWithOptions(HandlerOptions{ControlPlane: true, DataPlane: true})
}

func (s *Server) HandlerWithOptions(options HandlerOptions) http.Handler {
	r := chi.NewRouter()
	if options.ControlPlane {
		r.Get("/api/status", s.status)
		r.Get("/api/kernel/resolve", s.resolve)
		r.HandleFunc("/api/providers", s.providerCollection)
		r.HandleFunc("/api/connections", s.connectionsCollection)
		r.HandleFunc("/api/models", s.models)
		r.HandleFunc("/api/combos", s.combos)
		r.HandleFunc("/api/logical-models", s.logicalModels)
		r.HandleFunc("/api/custom-models", s.customModels)
		r.HandleFunc("/api/public-models", s.publicModels)
	}
	if options.DataPlane {
		r.Get("/v1/models", s.models)
		r.Post("/v1/chat/completions", s.chatCompletions)
		r.Post("/v1/responses", s.chatCompletions)
		r.Post("/v1/messages", s.chatCompletions)
	}
	return logging(r)
}

func (s *Server) Status() map[string]any {
	version := uint64(0)
	if s.control != nil {
		version = s.control.Version()
	}
	return map[string]any{"name": "gobroomd", "status": "ok", "snapshotVersion": version}
}
func (s *Server) Control() *controlplane.Manager { return s.control }
func (s *Server) SetExecutor(executor func(context.Context, normalize.Request, http.ResponseWriter) error) {
	s.executor = executor
}
func (s *Server) SetReloadHook(hook func() error) { s.reloadHook = hook }
func (s *Server) Reload() error {
	if s.control == nil {
		return fmt.Errorf("control plane unavailable")
	}
	if err := s.control.Reload(); err != nil {
		return err
	}
	if s.reloadHook != nil {
		return s.reloadHook()
	}
	return nil
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	version := uint64(0)
	if s.control != nil {
		version = s.control.Version()
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": "gobroomd", "status": "ok", "uptimeSeconds": int(time.Since(s.started).Seconds()), "snapshotVersion": version})
}

func (s *Server) reloadSnapshot(w http.ResponseWriter) bool {
	if s.control == nil {
		writeError(w, fmt.Errorf("control plane unavailable"))
		return false
	}
	if err := s.control.Reload(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("model")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	if s.control == nil {
		writeError(w, fmt.Errorf("control plane unavailable"))
		return
	}
	resolved, err := s.control.Resolve(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.ProviderNodes()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) providerCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.providers(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidateProviderDelete(id); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.DeleteProviderNode(id); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut {
		var input store.UpdateProviderNodeInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if input.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		if input.DefinitionID == "" {
			input.DefinitionID = "openai-compatible-chat"
		}
		prefixes := provider.NewPrefixRegistry()
		for _, node := range mustProviderNodes(s.store) {
			if node.ID != input.ID {
				if err := prefixes.AddCustom(node.Prefix, node.ID); err != nil {
					writeError(w, err)
					return
				}
			}
		}
		if input.Prefix != "" {
			if err := prefixes.AddNode(input.Prefix, input.DefinitionID); err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
		}
		item, err := s.store.UpdateProviderNode(input)
		if err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"provider": item})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input store.CreateProviderNodeInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if input.DefinitionID == "" {
		input.DefinitionID = "openai-compatible-chat"
	}
	prefixes := provider.NewPrefixRegistry()
	existing, err := s.store.ProviderNodes()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, node := range existing {
		if err := prefixes.AddCustom(node.Prefix, node.ID); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := prefixes.AddNode(input.Prefix, input.DefinitionID); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	node, err := s.store.CreateProviderNode(input)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.reloadSnapshot(w) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"provider": node})
}

func (s *Server) connectionsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.Connections(r.URL.Query().Get("nodeID"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"connections": items})
	case http.MethodPost:
		var input store.CreateConnectionInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		item, err := s.store.CreateConnection(input)
		if err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"connection": item})
	case http.MethodPut:
		var input store.UpdateConnectionInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if input.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		item, err := s.store.UpdateConnection(input)
		if err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"connection": item})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		if err := s.store.DeleteConnection(id); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func mustProviderNodes(s *store.Store) []store.ProviderNode {
	items, _ := s.ProviderNodes()
	return items
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.PublicModels()
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, item := range items {
		data = append(data, map[string]any{"id": item.Name, "object": "model", "created": 0, "owned_by": item.OwnedBy, "gobroom_target": item.TargetRef})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) combos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ComboDetails()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"combos": items})
	case http.MethodPost, http.MethodPut:
		var item store.ComboRecord
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidateCombo(item); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.UpsertCombo(item); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidateComboDelete(name); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.DeleteCombo(name); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) logicalModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.LogicalModels()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": items})
	case http.MethodPut:
		var item store.LogicalModelRecord
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidateLogicalModel(item); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.UpsertLogicalModel(item.Name, item.TargetRef); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidateLogicalModelDelete(name); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.DeleteLogicalModel(name); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) customModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.Models()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": items})
	case http.MethodPut:
		var item store.UpsertCatalogModelInput
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if item.Kind == "" {
			item.Kind = "custom"
		}
		if err := s.store.UpsertCatalogModel(item); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		if err := s.store.DeleteCatalogModel(id); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) publicModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.PublicModels()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": items})
	case http.MethodPut:
		var item store.PublicModel
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.TargetRef) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and targetRef are required"})
			return
		}
		if s.control != nil {
			if err := s.control.ValidatePublicModel(item); err != nil {
				writeError(w, err)
				return
			}
		}
		if err := s.store.UpsertPublicModel(item); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		if err := s.store.DeletePublicModel(name); err != nil {
			writeError(w, err)
			return
		}
		if !s.reloadSnapshot(w) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "cannot read request"}})
		return
	}
	normalized, err := normalize.JSON(r.URL.Path, r.Header, payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": err.Error()}})
		return
	}
	requestModel := normalized.Request.Model
	if s.executor != nil {
		tracked := &trackingWriter{ResponseWriter: w}
		if err := s.executor(r.Context(), normalized.Request, tracked); err != nil && !tracked.committed {
			status := http.StatusBadGateway
			if errors.Is(err, kernel.ErrModelNotPublished) {
				status = http.StatusNotFound
			}
			if errors.Is(err, kernel.ErrNoRoute) {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]any{"error": map[string]string{"message": err.Error(), "model": requestModel}})
		}
		return
	}
	items, err := s.store.PublicModels()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, item := range items {
		if item.Name == requestModel {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": map[string]string{"message": "public model resolved; provider execution is the next adapter milestone", "model": item.Name, "target": item.TargetRef}})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "model is not published", "model": requestModel}})
}

type trackingWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *trackingWriter) WriteHeader(status int) {
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackingWriter) Write(data []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *trackingWriter) Flush() {
	w.committed = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"error": map[string]string{"message": "routing execution is not implemented yet"}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) })
}
