package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorouter/gorouter/internal/normalize"
	"github.com/gorouter/gorouter/internal/store"
)

type Server struct {
	store   *store.Store
	started time.Time
}

func NewServer(s *store.Store) *Server { return &Server{store: s, started: time.Now()} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.status)
	mux.HandleFunc("/api/providers", s.providers)
	mux.HandleFunc("/api/models", s.models)
	mux.HandleFunc("/api/combos", s.combos)
	mux.HandleFunc("/api/public-models", s.publicModels)
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("/v1/responses", s.notImplemented)
	mux.HandleFunc("/v1/messages", s.notImplemented)
	return logging(mux)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "gorouterd", "status": "ok", "uptimeSeconds": int(time.Since(s.started).Seconds())})
}

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.ProviderNodes()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.PublicModels()
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, item := range items {
		data = append(data, map[string]any{"id": item.Name, "object": "model", "created": 0, "owned_by": item.OwnedBy, "gorouter_target": item.TargetRef})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) combos(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.Combos()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"combos": items})
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
		if err := s.store.UpsertPublicModel(item); err != nil {
			writeError(w, err)
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
