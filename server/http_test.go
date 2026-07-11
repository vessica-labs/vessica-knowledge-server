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
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := send("POST", "/v1/exports", "", `{"workspace_id":"kwsp_test"}`, true); got.Code != 401 {
		t.Fatalf("ordinary token exported=%d %s", got.Code, got.Body.String())
	}
	req := httptest.NewRequest("POST", "/v1/exports", bytes.NewBufferString(`{"workspace_id":"kwsp_test"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin export=%d %s", rec.Code, rec.Body.String())
	}
}
