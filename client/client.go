package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/vessica-labs/vessica-knowledge-server/knowledge"
)

type Client struct {
	BaseURL string
	Token   string
	Actor   knowledge.Actor
	HTTP    *http.Client
}
type envelope struct {
	OK    bool             `json:"ok"`
	Data  json.RawMessage  `json:"data"`
	Error *knowledge.Error `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path, key string, body, target any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Actor-ID", c.Actor.ID)
	req.Header.Set("X-Actor-Type", c.Actor.Type)
	req.Header.Set("X-Provenance-Source", "vessica_cli")
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("knowledge API %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if !e.OK {
		if e.Error != nil {
			return e.Error
		}
		return fmt.Errorf("knowledge API failed")
	}
	if target != nil {
		return json.Unmarshal(e.Data, target)
	}
	return nil
}
func (c *Client) Context(ctx context.Context, v knowledge.ContextRequest) (knowledge.ContextResponse, error) {
	var out knowledge.ContextResponse
	e := c.do(ctx, "POST", "/v1/context", "", v, &out)
	return out, e
}
func (c *Client) CreateScope(ctx context.Context, key string, v knowledge.Scope) (knowledge.Scope, error) {
	var out knowledge.Scope
	e := c.do(ctx, "POST", "/v1/scopes", key, v, &out)
	return out, e
}
func (c *Client) CreateEntity(ctx context.Context, key string, v knowledge.Entity) (knowledge.Entity, error) {
	var out knowledge.Entity
	e := c.do(ctx, "POST", "/v1/entities", key, v, &out)
	return out, e
}
func (c *Client) ResolveEntities(ctx context.Context, w, q string, scopes []string) ([]knowledge.Entity, error) {
	u := "/v1/entities:resolve?workspace_id=" + url.QueryEscape(w) + "&q=" + url.QueryEscape(q)
	for _, s := range scopes {
		u += "&scope=" + url.QueryEscape(s)
	}
	var out []knowledge.Entity
	e := c.do(ctx, "GET", u, "", nil, &out)
	return out, e
}
func (c *Client) ListEntities(ctx context.Context, w, typ, state, cursor string, limit int, scopes []string) (knowledge.Page[knowledge.Entity], error) {
	u := "/v1/entities?workspace_id=" + url.QueryEscape(w) + "&type=" + url.QueryEscape(typ) + "&state=" + url.QueryEscape(state) + "&cursor=" + url.QueryEscape(cursor) + "&limit=" + fmt.Sprint(limit)
	for _, s := range scopes {
		u += "&scope=" + url.QueryEscape(s)
	}
	var out knowledge.Page[knowledge.Entity]
	e := c.do(ctx, "GET", u, "", nil, &out)
	return out, e
}
func (c *Client) GetEntity(ctx context.Context, w, id string) (knowledge.Entity, error) {
	var out knowledge.Entity
	e := c.do(ctx, "GET", "/v1/entities/"+url.PathEscape(id)+"?workspace_id="+url.QueryEscape(w), "", nil, &out)
	return out, e
}
func (c *Client) Status(ctx context.Context, w string) (knowledge.Status, error) {
	var out knowledge.Status
	e := c.do(ctx, "GET", "/v1/status?workspace_id="+url.QueryEscape(w), "", nil, &out)
	return out, e
}
func (c *Client) Search(ctx context.Context, w, q, objectType, cursor string, limit int, scopes []string) (knowledge.Page[knowledge.SearchResult], error) {
	u := "/v1/search?workspace_id=" + url.QueryEscape(w) + "&q=" + url.QueryEscape(q) + "&object_type=" + url.QueryEscape(objectType) + "&cursor=" + url.QueryEscape(cursor) + "&limit=" + fmt.Sprint(limit)
	for _, s := range scopes {
		u += "&scope=" + url.QueryEscape(s)
	}
	var out knowledge.Page[knowledge.SearchResult]
	e := c.do(ctx, "GET", u, "", nil, &out)
	return out, e
}
func (c *Client) CreateArtifact(ctx context.Context, key string, v knowledge.Artifact) (knowledge.Artifact, error) {
	var out knowledge.Artifact
	e := c.do(ctx, "POST", "/v1/artifacts", key, v, &out)
	return out, e
}
func (c *Client) GetArtifact(ctx context.Context, w, id string, version int) (knowledge.Artifact, error) {
	var out knowledge.Artifact
	e := c.do(ctx, "GET", fmt.Sprintf("/v1/artifacts/%s?workspace_id=%s&version=%d", url.PathEscape(id), url.QueryEscape(w), version), "", nil, &out)
	return out, e
}
func (c *Client) ListArtifactVersions(ctx context.Context, w, id, cursor string, limit int) (knowledge.Page[knowledge.Artifact], error) {
	var out knowledge.Page[knowledge.Artifact]
	e := c.do(ctx, "GET", fmt.Sprintf("/v1/artifacts/%s/versions?workspace_id=%s&cursor=%s&limit=%d", url.PathEscape(id), url.QueryEscape(w), url.QueryEscape(cursor), limit), "", nil, &out)
	return out, e
}
func (c *Client) ListArtifacts(ctx context.Context, w, typ, status string, scopes []string) ([]knowledge.Artifact, error) {
	u := "/v1/artifacts?workspace_id=" + url.QueryEscape(w) + "&type=" + url.QueryEscape(typ) + "&status=" + url.QueryEscape(status)
	for _, s := range scopes {
		u += "&scope=" + url.QueryEscape(s)
	}
	var out []knowledge.Artifact
	e := c.do(ctx, "GET", u, "", nil, &out)
	return out, e
}
func (c *Client) VersionArtifact(ctx context.Context, key string, v knowledge.Artifact) (knowledge.Artifact, error) {
	var out knowledge.Artifact
	e := c.do(ctx, "POST", "/v1/artifacts/"+url.PathEscape(v.ID)+"/versions", key, v, &out)
	return out, e
}
func (c *Client) SetArtifactStatus(ctx context.Context, key, w, id, status string) (knowledge.Artifact, error) {
	action := status
	if status == "active" {
		action = "activate"
	} else if status == "superseded" {
		action = "supersede"
	}
	var out knowledge.Artifact
	e := c.do(ctx, "POST", "/v1/artifacts/"+url.PathEscape(id)+"/"+action, key, map[string]string{"workspace_id": w}, &out)
	return out, e
}
func (c *Client) CreateMemory(ctx context.Context, key string, v knowledge.Memory) (knowledge.Memory, error) {
	var out knowledge.Memory
	e := c.do(ctx, "POST", "/v1/memories", key, v, &out)
	return out, e
}
func (c *Client) GetMemory(ctx context.Context, w, id string, version int) (knowledge.Memory, error) {
	var out knowledge.Memory
	e := c.do(ctx, "GET", fmt.Sprintf("/v1/memories/%s?workspace_id=%s&version=%d", url.PathEscape(id), url.QueryEscape(w), version), "", nil, &out)
	return out, e
}
func (c *Client) ListMemoryVersions(ctx context.Context, w, id, cursor string, limit int) (knowledge.Page[knowledge.Memory], error) {
	var out knowledge.Page[knowledge.Memory]
	e := c.do(ctx, "GET", fmt.Sprintf("/v1/memories/%s/versions?workspace_id=%s&cursor=%s&limit=%d", url.PathEscape(id), url.QueryEscape(w), url.QueryEscape(cursor), limit), "", nil, &out)
	return out, e
}
func (c *Client) ListRelationships(ctx context.Context, w, objectID, cursor string, limit int) (knowledge.Page[knowledge.Relationship], error) {
	var out knowledge.Page[knowledge.Relationship]
	e := c.do(ctx, "GET", fmt.Sprintf("/v1/relationships?workspace_id=%s&object_id=%s&cursor=%s&limit=%d", url.QueryEscape(w), url.QueryEscape(objectID), url.QueryEscape(cursor), limit), "", nil, &out)
	return out, e
}
func (c *Client) SearchMemories(ctx context.Context, w, q string, scopes []string) ([]knowledge.Memory, error) {
	u := "/v1/memories?workspace_id=" + url.QueryEscape(w) + "&q=" + url.QueryEscape(q)
	for _, s := range scopes {
		u += "&scope=" + url.QueryEscape(s)
	}
	var out []knowledge.Memory
	e := c.do(ctx, "GET", u, "", nil, &out)
	return out, e
}
func (c *Client) VersionMemory(ctx context.Context, key string, v knowledge.Memory) (knowledge.Memory, error) {
	var out knowledge.Memory
	e := c.do(ctx, "POST", "/v1/memories/"+url.PathEscape(v.ID)+"/versions", key, v, &out)
	return out, e
}
func (c *Client) SetMemoryState(ctx context.Context, key, w, id, state string) (knowledge.Memory, error) {
	var out knowledge.Memory
	e := c.do(ctx, "POST", "/v1/memories/"+url.PathEscape(id)+"/"+state, key, map[string]string{"workspace_id": w}, &out)
	return out, e
}
func (c *Client) CreateRelationship(ctx context.Context, key string, v knowledge.Relationship) (knowledge.Relationship, error) {
	var out knowledge.Relationship
	e := c.do(ctx, "POST", "/v1/relationships", key, v, &out)
	return out, e
}
func (c *Client) IngestWorkflowEvent(ctx context.Context, key string, v knowledge.WorkflowEvent) (knowledge.Memory, error) {
	var out knowledge.Memory
	e := c.do(ctx, "POST", "/v1/workflow-events", key, v, &out)
	return out, e
}
func (c *Client) Export(ctx context.Context, w string) (knowledge.Snapshot, error) {
	var out knowledge.Snapshot
	e := c.do(ctx, "POST", "/v1/exports", "", map[string]string{"workspace_id": w}, &out)
	return out, e
}
func (c *Client) Import(ctx context.Context, v knowledge.Snapshot) error {
	return c.do(ctx, "POST", "/v1/imports", "import-"+v.Checksum, v, nil)
}
