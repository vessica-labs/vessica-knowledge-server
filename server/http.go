package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vessica-labs/vessica-knowledge-server/knowledge"
)

type Server struct {
	Service          *knowledge.Service
	Token            string
	DefaultWorkspace string
	rateMu           sync.Mutex
	rate             map[string]rateWindow
	imports          map[string]map[string]any
}
type rateWindow struct {
	Started time.Time
	Count   int
}
type envelope struct {
	Schema string           `json:"schema"`
	OK     bool             `json:"ok"`
	Data   any              `json:"data,omitempty"`
	Error  *knowledge.Error `json:"error,omitempty"`
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", s.health)
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("POST /v1/context", s.auth(s.context))
	m.HandleFunc("POST /v1/scopes", s.auth(s.createScope))
	m.HandleFunc("GET /v1/scopes/{id}", s.auth(s.getScope))
	m.HandleFunc("POST /v1/entities", s.auth(s.createEntity))
	m.HandleFunc("GET /v1/entities/{id}", s.auth(s.getEntity))
	m.HandleFunc("GET /v1/entities:resolve", s.auth(s.resolveEntities))
	m.HandleFunc("POST /v1/artifacts", s.auth(s.createArtifact))
	m.HandleFunc("GET /v1/artifacts/{id}", s.auth(s.getArtifact))
	m.HandleFunc("GET /v1/artifacts", s.auth(s.listArtifacts))
	m.HandleFunc("POST /v1/artifacts/{id}/versions", s.auth(s.versionArtifact))
	m.HandleFunc("POST /v1/artifacts/{id}/activate", s.auth(s.artifactStatus("active")))
	m.HandleFunc("POST /v1/artifacts/{id}/supersede", s.auth(s.artifactStatus("superseded")))
	m.HandleFunc("POST /v1/memories", s.auth(s.createMemory))
	m.HandleFunc("GET /v1/memories/{id}", s.auth(s.getMemory))
	m.HandleFunc("GET /v1/memories", s.auth(s.searchMemories))
	m.HandleFunc("POST /v1/memories/{id}/versions", s.auth(s.versionMemory))
	m.HandleFunc("POST /v1/memories/{id}/supersede", s.auth(s.memoryState("superseded")))
	m.HandleFunc("POST /v1/memories/{id}/archive", s.auth(s.memoryState("archived")))
	m.HandleFunc("POST /v1/relationships", s.auth(s.createRelationship))
	m.HandleFunc("POST /v1/workflow-events", s.auth(s.workflow))
	m.HandleFunc("POST /v1/exports", s.auth(s.export))
	m.HandleFunc("POST /v1/imports", s.auth(s.importSnapshot))
	m.HandleFunc("GET /v1/imports/{id}", s.auth(s.importStatus))
	// Go's ServeMux does not support a static suffix after a path wildcard.
	// Preserve the public colon-action API by rewriting it to internal routes.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for from, to := range map[string]string{":version": "/versions", ":activate": "/activate", ":supersede": "/supersede", ":archive": "/archive"} {
			if strings.HasSuffix(r.URL.Path, from) {
				r.URL.Path = strings.TrimSuffix(r.URL.Path, from) + to
				break
			}
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"service": "vessica-knowledge-server", "ok": true})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil || s.Service.Store.Ping(r.Context()) != nil {
		write(w, 503, map[string]any{"ok": false, "api": "ready", "database": "unavailable", "migrations": "ready", "embedding_worker": "unknown", "index": "unknown"})
		return
	}
	embedding := "not_configured"
	if s.Service.Embedder != nil {
		embedding = "ready"
	}
	write(w, 200, map[string]any{"ok": true, "api": "ready", "database": "ready", "migrations": "ready", "embedding_worker": embedding, "index": "ready"})
}
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && !token(r.Header.Get("Authorization"), s.Token) {
			failure(w, &knowledge.Error{Code: "unauthorized", Message: "invalid bearer token", Status: 401})
			return
		}
		if !s.allow(r.Header.Get("Authorization")) {
			failure(w, &knowledge.Error{Code: "rate_limited", Message: "request rate limit exceeded", Status: 429})
			return
		}
		next(w, r)
	}
}
func (s *Server) allow(key string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rate == nil {
		s.rate = map[string]rateWindow{}
	}
	now := time.Now()
	v := s.rate[key]
	if v.Started.IsZero() || now.Sub(v.Started) >= time.Minute {
		v = rateWindow{Started: now}
	}
	v.Count++
	s.rate[key] = v
	return v.Count <= 600
}
func token(h, want string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		failure(w, &knowledge.Error{Code: "invalid_json", Message: err.Error(), Status: 400})
		return false
	}
	return true
}
func (s *Server) opts(r *http.Request, workspace string) (knowledge.WriteOptions, error) {
	if workspace == "" {
		workspace = s.DefaultWorkspace
	}
	if s.DefaultWorkspace != "" && workspace != s.DefaultWorkspace {
		return knowledge.WriteOptions{}, &knowledge.Error{Code: "forbidden_workspace", Message: "token is not scoped to this workspace", Status: 403}
	}
	o := knowledge.WriteOptions{WorkspaceID: workspace, IdempotencyKey: r.Header.Get("Idempotency-Key"), Actor: knowledge.Actor{ID: r.Header.Get("X-Actor-ID"), Type: r.Header.Get("X-Actor-Type")}, Provenance: knowledge.Provenance{Source: r.Header.Get("X-Provenance-Source"), SourceID: r.Header.Get("X-Provenance-ID")}}
	if o.Actor.ID == "" {
		o.Actor.ID = "api"
	}
	if o.Actor.Type == "" {
		o.Actor.Type = "service"
	}
	if o.Provenance.Source == "" {
		o.Provenance.Source = "api"
	}
	if o.IdempotencyKey == "" {
		return o, knowledge.Invalid("Idempotency-Key header required")
	}
	return o, nil
}
func workspace(r *http.Request, def string) string {
	if def != "" {
		return def
	}
	if v := r.URL.Query().Get("workspace_id"); v != "" {
		return v
	}
	return def
}
func (s *Server) context(w http.ResponseWriter, r *http.Request) {
	var v knowledge.ContextRequest
	if !decode(w, r, &v) {
		return
	}
	if v.WorkspaceID == "" {
		v.WorkspaceID = s.DefaultWorkspace
	}
	if s.DefaultWorkspace != "" && v.WorkspaceID != s.DefaultWorkspace {
		failure(w, &knowledge.Error{Code: "forbidden_workspace", Message: "token is not scoped to this workspace", Status: 403})
		return
	}
	got, err := s.Service.Context(r.Context(), v)
	respond(w, got, err, 200)
}
func (s *Server) createScope(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Scope
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.CreateScope(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) getScope(w http.ResponseWriter, r *http.Request) {
	got, e := s.Service.Store.GetScope(r.Context(), workspace(r, s.DefaultWorkspace), r.PathValue("id"))
	respond(w, got, e, 200)
}
func (s *Server) createEntity(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Entity
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.CreateEntity(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	got, e := s.Service.Store.GetEntity(r.Context(), workspace(r, s.DefaultWorkspace), r.PathValue("id"))
	respond(w, got, e, 200)
}
func (s *Server) resolveEntities(w http.ResponseWriter, r *http.Request) {
	got, e := s.Service.Store.ResolveEntities(r.Context(), workspace(r, s.DefaultWorkspace), r.URL.Query()["scope"], r.URL.Query().Get("q"))
	respond(w, got, e, 200)
}
func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Artifact
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.CreateArtifact(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) versionArtifact(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Artifact
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.VersionArtifact(r.Context(), o, r.PathValue("id"), v)
	respond(w, got, e, 200)
}
func (s *Server) artifactStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WorkspaceID string `json:"workspace_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		o, e := s.opts(r, body.WorkspaceID)
		if e != nil {
			failure(w, e)
			return
		}
		got, e := s.Service.SetArtifactStatus(r.Context(), o, r.PathValue("id"), status)
		respond(w, got, e, 200)
	}
}
func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("version"))
	got, e := s.Service.Store.GetArtifact(r.Context(), workspace(r, s.DefaultWorkspace), r.PathValue("id"), n)
	respond(w, got, e, 200)
}
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	sel := knowledge.ArtifactSelector{Type: r.URL.Query().Get("type"), Status: r.URL.Query().Get("status")}
	got, e := s.Service.Store.ListArtifacts(r.Context(), workspace(r, s.DefaultWorkspace), r.URL.Query()["scope"], []knowledge.ArtifactSelector{sel})
	respond(w, got, e, 200)
}
func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Memory
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.CreateMemory(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) versionMemory(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Memory
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.VersionMemory(r.Context(), o, r.PathValue("id"), v)
	respond(w, got, e, 200)
}
func (s *Server) memoryState(state string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WorkspaceID string `json:"workspace_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		o, e := s.opts(r, body.WorkspaceID)
		if e != nil {
			failure(w, e)
			return
		}
		got, e := s.Service.SetMemoryState(r.Context(), o, r.PathValue("id"), state)
		respond(w, got, e, 200)
	}
}
func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("version"))
	got, e := s.Service.Store.GetMemory(r.Context(), workspace(r, s.DefaultWorkspace), r.PathValue("id"), n)
	respond(w, got, e, 200)
}
func (s *Server) searchMemories(w http.ResponseWriter, r *http.Request) {
	got, e := s.Service.Store.SearchMemories(r.Context(), workspace(r, s.DefaultWorkspace), r.URL.Query()["scope"], r.URL.Query().Get("q"), 100)
	respond(w, got, e, 200)
}
func (s *Server) createRelationship(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Relationship
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.CreateRelationship(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) workflow(w http.ResponseWriter, r *http.Request) {
	var v knowledge.WorkflowEvent
	if !decode(w, r, &v) {
		return
	}
	o, e := s.opts(r, v.WorkspaceID)
	if e != nil {
		failure(w, e)
		return
	}
	got, e := s.Service.IngestWorkflowEvent(r.Context(), o, v)
	respond(w, got, e, 201)
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.DefaultWorkspace != "" && body.WorkspaceID != s.DefaultWorkspace {
		failure(w, &knowledge.Error{Code: "forbidden_workspace", Message: "token is not scoped to this workspace", Status: 403})
		return
	}
	got, e := s.Service.Store.Export(r.Context(), body.WorkspaceID)
	respond(w, got, e, 200)
}
func (s *Server) importSnapshot(w http.ResponseWriter, r *http.Request) {
	var v knowledge.Snapshot
	if !decode(w, r, &v) {
		return
	}
	if s.DefaultWorkspace != "" && v.WorkspaceID != s.DefaultWorkspace {
		failure(w, &knowledge.Error{Code: "forbidden_workspace", Message: "token is not scoped to this workspace", Status: 403})
		return
	}
	e := s.Service.ImportSnapshot(r.Context(), v)
	id := "import-" + v.Checksum
	status := map[string]any{"id": id, "status": map[bool]string{true: "completed", false: "failed"}[e == nil], "imported": e == nil, "counts": v.Counts, "checksum": v.Checksum}
	s.rateMu.Lock()
	if s.imports == nil {
		s.imports = map[string]map[string]any{}
	}
	s.imports[id] = status
	s.rateMu.Unlock()
	respond(w, status, e, 201)
}
func (s *Server) importStatus(w http.ResponseWriter, r *http.Request) {
	s.rateMu.Lock()
	v, ok := s.imports[r.PathValue("id")]
	s.rateMu.Unlock()
	if !ok {
		failure(w, knowledge.NotFound("import not found"))
		return
	}
	respond(w, v, nil, 200)
}
func respond(w http.ResponseWriter, data any, err error, status int) {
	if err != nil {
		failure(w, err)
		return
	}
	writeEnvelope(w, status, envelope{Schema: knowledge.APIVersion, OK: true, Data: data})
}
func failure(w http.ResponseWriter, err error) {
	var ke *knowledge.Error
	if !errors.As(err, &ke) {
		ke = &knowledge.Error{Code: "internal", Message: err.Error(), Status: 500}
	}
	writeEnvelope(w, ke.Status, envelope{Schema: knowledge.APIVersion, OK: false, Error: ke})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeEnvelope(w http.ResponseWriter, status int, v envelope) { write(w, status, v) }

var _ = io.EOF
