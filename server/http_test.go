package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vessica-labs/vessica-knowledge-server/knowledge"
)

func TestAPIAuthIdempotencyAndContext(t *testing.T) {
	store, err := knowledge.OpenSQLite(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := knowledge.NewService(store, nil)
	api := (&Server{Service: svc, Token: "secret", ExportToken: "admin-secret", DefaultWorkspace: "kwsp_test"}).Handler()
	send := func(method, path, key, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if auth {
			req.Header.Set("Authorization", "Bearer secret")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Actor-ID", "test-user")
		req.Header.Set("X-Actor-Type", "human")
		req.Header.Set("X-Provenance-Source", "test")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	if got := send("POST", "/v1/scopes", "scope", `{"type":"repository","name":"Repo","canonical_key":"repo"}`, false); got.Code != 401 {
		t.Fatalf("unauth=%d", got.Code)
	}
	created := send("POST", "/v1/scopes", "scope", `{"type":"repository","name":"Repo","canonical_key":"repo"}`, true)
	if created.Code != 201 {
		t.Fatalf("scope=%d %s", created.Code, created.Body.String())
	}
	var env struct {
		Data knowledge.Scope `json:"data"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &env)
	replay := send("POST", "/v1/scopes", "scope", `{"type":"repository","name":"Different","canonical_key":"different"}`, true)
	if replay.Code != 201 || !bytes.Contains(replay.Body.Bytes(), []byte(env.Data.ID)) {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
	ctxBody, _ := json.Marshal(knowledge.ContextRequest{WorkspaceID: "kwsp_test", Query: "anything", ScopeIDs: []string{env.Data.ID}})
	response := send("POST", "/v1/context", "", string(ctxBody), true)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"retrieval_mode":"lexical"`)) {
		t.Fatalf("context=%d %s", response.Code, response.Body.String())
	}
	forbiddenBody, _ := json.Marshal(knowledge.ContextRequest{WorkspaceID: "another", Query: "anything"})
	if got := send("POST", "/v1/context", "", string(forbiddenBody), true); got.Code != 403 {
		t.Fatalf("workspace escape=%d %s", got.Code, got.Body.String())
	}
	artifact := send("POST", "/v1/artifacts", "artifact", `{"scope_id":"`+env.Data.ID+`","type":"adr","title":"ADR","content":"body"}`, true)
	if artifact.Code != 201 {
		t.Fatalf("artifact=%d %s", artifact.Code, artifact.Body.String())
	}
	var artifactEnv struct {
		Data knowledge.Artifact `json:"data"`
	}
	_ = json.Unmarshal(artifact.Body.Bytes(), &artifactEnv)
	version := send("POST", "/v1/artifacts/"+artifactEnv.Data.ID+":version", "artifact-v2", `{"content":"body two"}`, true)
	if version.Code != 200 || !bytes.Contains(version.Body.Bytes(), []byte(`"version":2`)) {
		t.Fatalf("colon version=%d %s", version.Code, version.Body.String())
	}
	if got := send("GET", "/v1/artifacts/"+artifactEnv.Data.ID+"/versions?limit=1", "", "", true); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(`"next_cursor"`)) {
		t.Fatalf("artifact versions=%d %s", got.Code, got.Body.String())
	}
	entity := send("POST", "/v1/entities", "entity", `{"scope_id":"`+env.Data.ID+`","type":"repository","display_name":"Demo repository","aliases":["demo"]}`, true)
	if entity.Code != 201 {
		t.Fatalf("entity=%d %s", entity.Code, entity.Body.String())
	}
	var entityEnv struct {
		Data knowledge.Entity `json:"data"`
	}
	_ = json.Unmarshal(entity.Body.Bytes(), &entityEnv)
	if got := send("GET", "/v1/entities?type=repository&limit=20", "", "", true); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(entityEnv.Data.ID)) {
		t.Fatalf("entities=%d %s", got.Code, got.Body.String())
	}
	memory := send("POST", "/v1/memories", "memory", `{"scope_id":"`+env.Data.ID+`","type":"fact","title":"Dashboard fact","content":"The dashboard is embedded","importance":0.7,"confidence":0.9,"confidence_source":"human_confirmed"}`, true)
	if memory.Code != 201 {
		t.Fatalf("memory=%d %s", memory.Code, memory.Body.String())
	}
	if !bytes.Contains(memory.Body.Bytes(), []byte(`"embedding_state":"not_configured"`)) {
		t.Fatalf("lexical memory state=%s", memory.Body.String())
	}
	var memoryEnv struct {
		Data knowledge.Memory `json:"data"`
	}
	_ = json.Unmarshal(memory.Body.Bytes(), &memoryEnv)
	if got := send("POST", "/v1/memories/"+memoryEnv.Data.ID+":version", "memory-v2", `{"content":"The dashboard is securely embedded"}`, true); got.Code != 200 {
		t.Fatalf("memory version=%d %s", got.Code, got.Body.String())
	}
	rel := send("POST", "/v1/relationships", "relationship", `{"scope_id":"`+env.Data.ID+`","from_type":"entity","from_id":"`+entityEnv.Data.ID+`","predicate":"documented_by","to_type":"artifact","to_id":"`+artifactEnv.Data.ID+`","confidence":1}`, true)
	if rel.Code != 201 {
		t.Fatalf("relationship=%d %s", rel.Code, rel.Body.String())
	}
	for _, path := range []string{"/v1/relationships?object_id=" + entityEnv.Data.ID, "/v1/search?q=dashboard&limit=20", "/v1/status"} {
		if got := send("GET", path, "", "", true); got.Code != 200 {
			t.Fatalf("read endpoint %s=%d %s", path, got.Code, got.Body.String())
		}
	}
	search := send("GET", "/v1/search?q=dashboard&limit=20", "", "", true)
	var searchEnv struct {
		Data knowledge.Page[knowledge.SearchResult] `json:"data"`
	}
	if err := json.Unmarshal(search.Body.Bytes(), &searchEnv); err != nil || len(searchEnv.Data.Items) == 0 || searchEnv.Data.Items[0].ObjectType != "memory" {
		t.Fatalf("search was not globally recency ordered: %s err=%v", search.Body.String(), err)
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := send("POST", "/v1/exports", "", `{"workspace_id":"kwsp_test"}`, true); got.Code != 401 {
		t.Fatalf("ordinary token exported=%d %s", got.Code, got.Body.String())
	}
	backfillReq := httptest.NewRequest("POST", "/admin/v1/embeddings/backfill", bytes.NewBufferString(`{"mode":"missing"}`))
	backfillReq.Header.Set("Authorization", "Bearer admin-secret")
	backfill := httptest.NewRecorder()
	api.ServeHTTP(backfill, backfillReq)
	if backfill.Code != 202 || !bytes.Contains(backfill.Body.Bytes(), []byte(`"status":"catching_up"`)) {
		t.Fatalf("backfill=%d %s", backfill.Code, backfill.Body.String())
	}
	var backfillEnvelope struct {
		Data knowledge.EmbeddingBackfill `json:"data"`
	}
	if err := json.Unmarshal(backfill.Body.Bytes(), &backfillEnvelope); err != nil || backfillEnvelope.Data.JobID == "" {
		t.Fatalf("backfill job=%s err=%v", backfill.Body.String(), err)
	}
	statusReq := httptest.NewRequest("GET", "/admin/v1/embeddings/backfill/"+backfillEnvelope.Data.JobID, nil)
	statusReq.Header.Set("Authorization", "Bearer admin-secret")
	statusRec := httptest.NewRecorder()
	api.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != 200 || !bytes.Contains(statusRec.Body.Bytes(), []byte(`"job_id":"`+backfillEnvelope.Data.JobID+`"`)) {
		t.Fatalf("backfill status=%d %s", statusRec.Code, statusRec.Body.String())
	}
	req := httptest.NewRequest("POST", "/v1/exports", bytes.NewBufferString(`{"workspace_id":"kwsp_test"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin export=%d %s", rec.Code, rec.Body.String())
	}
}
