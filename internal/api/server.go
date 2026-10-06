package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	store            *store.Store
	control          *controlplane.Manager
	started          time.Time
	executor         func(context.Context, normalize.Request, http.ResponseWriter) error
	reloadHook       func() error
	dataPlaneToken   string
	ingress          map[string]OperationIngress
	providerCatalog  []provider.DefinitionMetadata
	extensionCatalog extensions.CatalogView
	operations       *operations.Snapshot
}

type HandlerOptions struct {
	ControlPlane bool
	DataPlane    bool
}

func NewServer(s *store.Store) *Server {
	return NewServerWithRuntimeBindings(s, nil)
}

func NewServerWithRuntimeBindings(s *store.Store, bindings map[string]provider.RuntimeBinding) *Server {
	manager, err := controlplane.NewManagerWithRuntimeBindings(s, bindings)
	if err != nil {
		manager = nil
	}
	operationSnapshot, _ := operations.BuiltinSnapshot()
	server := &Server{store: s, control: manager, started: time.Now(), ingress: map[string]OperationIngress{}, operations: operationSnapshot}
	_ = server.RegisterOperationIngress(JSONOperationIngress{CodecID: "openai-chat-ingress", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/chat/completions"}}})
	_ = server.RegisterOperationIngress(JSONOperationIngress{CodecID: "openai-responses-ingress", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/responses"}}})
	_ = server.RegisterOperationIngress(JSONOperationIngress{CodecID: "anthropic-messages-ingress", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/messages"}}})
	return server
}

// RegisterOperationIngress adds one exact client endpoint during daemon
// wiring. All registered operations use the same authentication, normalization
// handoff, kernel executor and error boundary.
func (s *Server) RegisterOperationIngress(codec OperationIngress) error {
	if err := validateOperationIngress(codec); err != nil {
		return err
	}
	if s.operations == nil {
		return fmt.Errorf("operation catalog is unavailable")
	}
	if _, ok := s.operations.Resolve(codec.OperationRef()); !ok {
		return fmt.Errorf("operation %q contract %d is not registered", codec.OperationRef().ID, codec.OperationRef().ContractVersion)
	}
	if s.ingress == nil {
		s.ingress = map[string]OperationIngress{}
	}
	for _, route := range codec.Routes() {
		key := strings.ToUpper(strings.TrimSpace(route.Method)) + " " + route.Path
		if key == "GET /v1/models" {
			return fmt.Errorf("operation ingress route %q is reserved", key)
		}
		if previous, exists := s.ingress[key]; exists {
			return fmt.Errorf("operation ingress route %q is already registered by %q", key, previous.ID())
		}
	}
	for _, route := range codec.Routes() {
		key := strings.ToUpper(strings.TrimSpace(route.Method)) + " " + route.Path
		s.ingress[key] = codec
	}
	return nil
}

func (s *Server) SetOperationSnapshot(snapshot *operations.Snapshot) error {
	if s == nil || snapshot == nil {
		return fmt.Errorf("server and operation snapshot are required")
	}
	for _, codec := range s.ingress {
		if _, ok := snapshot.Resolve(codec.OperationRef()); !ok {
			return fmt.Errorf("ingress codec %q references missing operation %s", codec.ID(), codec.OperationRef().Key())
		}
	}
	s.operations = snapshot
	return nil
}

func (s *Server) Handler() http.Handler {
	return s.HandlerWithOptions(HandlerOptions{ControlPlane: true, DataPlane: true})
}

func (s *Server) HandlerWithOptions(options HandlerOptions) http.Handler {
	r := chi.NewRouter()
	if options.ControlPlane {
		r.Get("/api/status", s.status)
		r.Get("/api/kernel/resolve", s.resolve)
		r.Get("/api/provider-definitions", s.providerDefinitions)
		r.Get("/api/extensions", s.extensions)
		r.HandleFunc("/api/providers", s.providerCollection)
		r.HandleFunc("/api/connections", s.connectionsCollection)
		r.HandleFunc("/api/models", s.models)
		r.HandleFunc("/api/custom-models", s.customModels)
	}
	if options.DataPlane {
		r.Get("/v1/models", s.dataPlane(s.models))
		routes := make([]string, 0, len(s.ingress))
		for route := range s.ingress {
			routes = append(routes, route)
		}
		sort.Strings(routes)
		for _, route := range routes {
			parts := strings.SplitN(route, " ", 2)
			method, path, codec := parts[0], parts[1], s.ingress[route]
			r.Method(method, path, s.dataPlane(s.operationHandler(codec)))
		}
	}
	return logging(r)
}

// SetProviderDefinitionCatalog installs the secret-free setup metadata shared
// by HTTP and IPC control clients.
func (s *Server) SetProviderDefinitionCatalog(catalog []provider.DefinitionMetadata) {
	if s == nil {
		return
	}
	s.providerCatalog = append([]provider.DefinitionMetadata(nil), catalog...)
}

func (s *Server) providerDefinitions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"definitions": s.providerCatalog})
}

func (s *Server) SetExtensionCatalog(catalog extensions.CatalogView) {
	if s != nil {
		s.extensionCatalog = catalog
	}
}

func (s *Server) extensions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.extensionCatalog)
}

func (s *Server) SetDataPlaneToken(token string) { s.dataPlaneToken = token }
func (s *Server) dataPlane(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.dataPlaneToken != "" && r.Header.Get("Authorization") != "Bearer "+s.dataPlaneToken {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "data plane authentication required"}})
			return
		}
		next(w, r)
	}
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
			if input.Protocol != "" {
				input.DefinitionID = store.DefaultDefinitionForProtocol(input.Protocol)
			} else if current, err := s.store.ProviderNode(input.ID); err == nil {
				input.DefinitionID = current.DefinitionID
			}
		}
		prefixes := provider.NewPrefixRegistry()
		for _, node := range mustProviderNodes(s.store) {
			if node.ID != input.ID {
				if err := prefixes.AddNode(node.Prefix, node.DefinitionID); err != nil {
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
		input.DefinitionID = store.DefaultDefinitionForProtocol(input.Protocol)
	}
	prefixes := provider.NewPrefixRegistry()
	existing, err := s.store.ProviderNodes()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, node := range existing {
		if err := prefixes.AddNode(node.Prefix, node.DefinitionID); err != nil {
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
	public := map[string]kernel.PublicModel{}
	if s.control != nil {
		public = s.control.Snapshot().PublicModels
	}
	names := make([]string, 0, len(public))
	for name := range public {
		names = append(names, name)
	}
	sort.Strings(names)
	data := make([]map[string]any, 0, len(names))
	for _, name := range names {
		item := public[name]
		data = append(data, map[string]any{"id": item.Name, "object": "model", "created": 0, "owned_by": item.OwnedBy, "gobroom_target": item.TargetRef})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
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

const maxIngressBodyBytes = 32 << 20

func (s *Server) operationHandler(codec OperationIngress) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxIngressBodyBytes)
		request, err := codec.Decode(r)
		if err != nil {
			status := http.StatusBadRequest
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				status = http.StatusRequestEntityTooLarge
			}
			writeJSON(w, status, map[string]any{"error": map[string]string{"message": err.Error()}})
			return
		}
		if request.Operation == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "ingress codec returned no semantic operation"}})
			return
		}
		s.executeNormalized(w, r, request)
	}
}

func (s *Server) executeNormalized(w http.ResponseWriter, r *http.Request, request normalize.Request) {
	requestModel := request.Model
	if s.executor != nil {
		tracked := &trackingWriter{ResponseWriter: w}
		if err := s.executor(r.Context(), request, tracked); err != nil && !tracked.committed {
			status := http.StatusBadGateway
			var operationInput *operations.InputError
			if errors.As(err, &operationInput) {
				status = http.StatusBadRequest
			}
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
	if s.control != nil {
		if item, ok := s.control.Snapshot().PublicModels[requestModel]; ok {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": map[string]string{"message": "model is exposed; provider execution is not configured", "model": item.Name, "target": item.TargetRef}})
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
