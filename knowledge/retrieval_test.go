package knowledge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type constantEmbedder struct{}

func (constantEmbedder) Provider() string { return "fake" }
func (constantEmbedder) Model() string    { return "fake-v2" }
func (constantEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

type reverseReranker struct{ invalid bool }

func (reverseReranker) Model() string { return "fake-reranker" }
func (r reverseReranker) Rerank(_ context.Context, _ string, memories []Memory) (RerankResult, error) {
	if r.invalid {
		return RerankResult{IDs: []string{"unknown"}}, nil
	}
	ids := make([]string, len(memories))
	for index := range memories {
		ids[index] = memories[len(memories)-1-index].ID
	}
	return RerankResult{IDs: ids, InputTokens: 10, OutputTokens: 3}, nil
}

func TestRetrieveMemoriesFiltersScopeLifecycleVersionAndValidity(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store, constantEmbedder{})
	workspace := "kwsp_retrieval"
	options := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: workspace, IdempotencyKey: key, Actor: Actor{ID: "tester", Type: "human"}, Provenance: Provenance{Source: "test"}}
	}
	scopeA, _ := service.CreateScope(ctx, options("scope-a"), Scope{Type: "repository", Name: "A", CanonicalKey: "a"})
	scopeB, _ := service.CreateScope(ctx, options("scope-b"), Scope{Type: "repository", Name: "B", CanonicalKey: "b"})
	current, _ := service.CreateMemory(ctx, options("current"), Memory{ScopeID: scopeA.ID, Type: "decision", Subject: "Project Alder", Title: "Alder access", Content: "Review links expire after 12 hours", Importance: .8, Confidence: 1})
	current, err = service.VersionMemory(ctx, options("current-v2"), current.ID, Memory{Content: "Review links expire after 18 hours"})
	if err != nil {
		t.Fatal(err)
	}
	wrongScope, _ := service.CreateMemory(ctx, options("wrong-scope"), Memory{ScopeID: scopeB.ID, Type: "decision", Title: "Alder access", Content: "Review links expire after seven days", Importance: 1, Confidence: 1})
	expiredAt := time.Now().Add(-time.Hour)
	expired, _ := service.CreateMemory(ctx, options("expired"), Memory{ScopeID: scopeA.ID, Type: "fact", Title: "Alder temporary", Content: "Review links expire after 36 hours", ValidUntil: &expiredAt, Importance: 1, Confidence: 1})
	archived, _ := service.CreateMemory(ctx, options("archived"), Memory{ScopeID: scopeA.ID, Type: "fact", Title: "Alder old", Content: "Review links expire after 72 hours", Importance: 1, Confidence: 1})
	archived, _ = service.SetMemoryState(ctx, options("archive"), archived.ID, "archived")
	for _, memory := range []Memory{current, wrongScope, expired, archived} {
		if err := store.PutEmbedding(ctx, workspace, "memory", memory.ID, memory.Version, []float32{1, 0, 0}, "fake", "fake-v2"); err != nil {
			t.Fatal(err)
		}
		if err := store.SetEmbeddingState(ctx, workspace, memory.ID, memory.Version, "ready"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.RetrieveMemories(ctx, MemoryRetrievalRequest{WorkspaceID: workspace, Query: "Project Alder review link lifetime", ScopeIDs: []string{scopeA.ID}, Limit: 20, Rerank: "never"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Memory.ID != current.ID || result.Results[0].Memory.Version != 2 {
		t.Fatalf("retrieval leaked stale, invalid, or wrong-scope memory: %#v", result.Results)
	}
	if result.RetrievalMode != "semantic_hybrid" || result.Ranking.Version != "v2" {
		t.Fatalf("retrieval metadata=%#v", result)
	}
}

func TestRetrieveMemoriesRerankerIsBoundedAndFallsBack(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store, nil).WithReranker(reverseReranker{})
	workspace := "kwsp_rerank"
	options := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: workspace, IdempotencyKey: key, Actor: Actor{ID: "tester", Type: "human"}, Provenance: Provenance{Source: "test"}}
	}
	scope, _ := service.CreateScope(ctx, options("scope"), Scope{Type: "repository", Name: "A", CanonicalKey: "a"})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		_, _ = service.CreateMemory(ctx, options(name), Memory{ScopeID: scope.ID, Type: "fact", Title: name + " project", Content: "shared review access", Importance: .5, Confidence: 1})
	}
	result, err := service.RetrieveMemories(ctx, MemoryRetrievalRequest{WorkspaceID: workspace, Query: "shared review access", ScopeIDs: []string{scope.ID}, Rerank: "always"})
	if err != nil || !result.Rerank.Applied || result.RetrievalMode != "lexical" || len(result.Results) != 3 {
		t.Fatalf("reranked=%#v err=%v", result, err)
	}
	service.WithReranker(reverseReranker{invalid: true})
	fallback, err := service.RetrieveMemories(ctx, MemoryRetrievalRequest{WorkspaceID: workspace, Query: "shared review access", ScopeIDs: []string{scope.ID}, Rerank: "always"})
	if err != nil || fallback.Rerank.Applied || fallback.Rerank.Reason != "invalid_provider_result" {
		t.Fatalf("fallback=%#v err=%v", fallback, err)
	}
}

func TestRetrieveMemoriesReportsAmbiguityAndHonorsExplicitEntity(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store, nil)
	workspace := "kwsp_personas"
	options := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: workspace, IdempotencyKey: key, Actor: Actor{ID: "tester", Type: "human"}, Provenance: Provenance{Source: "test"}}
	}
	scope, _ := service.CreateScope(ctx, options("scope"), Scope{Type: "repository", Name: "A", CanonicalKey: "a"})
	west, _ := service.CreateEntity(ctx, options("west"), Entity{ScopeID: scope.ID, Type: "person", DisplayName: "West Persona", Aliases: []string{"synthetic persona"}})
	east, _ := service.CreateEntity(ctx, options("east"), Entity{ScopeID: scope.ID, Type: "person", DisplayName: "East Persona", Aliases: []string{"synthetic persona"}})
	westMemory, _ := service.CreateMemory(ctx, options("west-memory"), Memory{ScopeID: scope.ID, Type: "instruction", Subject: "West Persona", Title: "West Persona scheduling", Content: "External meetings start after 11 AM", Importance: .8, Confidence: 1})
	eastMemory, _ := service.CreateMemory(ctx, options("east-memory"), Memory{ScopeID: scope.ID, Type: "instruction", Subject: "East Persona", Title: "East Persona scheduling", Content: "External meetings start after 9 AM", Importance: .8, Confidence: 1})
	for index, pair := range []struct {
		entity Entity
		memory Memory
	}{{west, westMemory}, {east, eastMemory}} {
		_, err = service.CreateRelationship(ctx, options("relationship-"+string(rune('a'+index))), Relationship{ScopeID: scope.ID, FromType: "memory", FromID: pair.memory.ID, Predicate: "about", ToType: "entity", ToID: pair.entity.ID, Confidence: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	ambiguous, err := service.RetrieveMemories(ctx, MemoryRetrievalRequest{WorkspaceID: workspace, Query: "synthetic persona scheduling policy", ScopeIDs: []string{scope.ID}, Rerank: "never"})
	if err != nil || ambiguous.Ambiguity != "ambiguous_subject" {
		t.Fatalf("ambiguous=%#v err=%v", ambiguous, err)
	}
	selected, err := service.RetrieveMemories(ctx, MemoryRetrievalRequest{WorkspaceID: workspace, Query: "scheduling", ScopeIDs: []string{scope.ID}, EntityIDs: []string{west.ID}, Rerank: "never"})
	if err != nil || selected.Ambiguity != "" || len(selected.Results) != 1 || selected.Results[0].Memory.ID != westMemory.ID {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
}

func TestContextReservesBudgetForRelevantDurableMemory(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store, nil)
	workspace := "kwsp_context_budget"
	options := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: workspace, IdempotencyKey: key, Actor: Actor{ID: "tester", Type: "human"}, Provenance: Provenance{Source: "test"}}
	}
	scope, _ := service.CreateScope(ctx, options("scope"), Scope{Type: "repository", Name: "A", CanonicalKey: "a"})
	memory, _ := service.CreateMemory(ctx, options("memory"), Memory{ScopeID: scope.ID, Type: "decision", Title: "Project Alder audience", Content: "Project Alder serves regional clinic coordinators", Importance: .8, Confidence: 1})
	for index := 0; index < 40; index++ {
		_, err = service.CreateArtifact(ctx, options("artifact-"+string(rune('a'+index))), Artifact{ScopeID: scope.ID, Type: "report", Title: "Unrelated report", Status: "active", Content: strings.Repeat("unrelated historical operations material ", 40)})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.Context(ctx, ContextRequest{WorkspaceID: workspace, Query: "Project Alder audience", ScopeIDs: []string{scope.ID}, ArtifactSelectors: []ArtifactSelector{{Status: "active"}}, TokenBudget: 4000})
	if err != nil || len(result.Decisions) != 1 || result.Decisions[0].Memory.ID != memory.ID {
		t.Fatalf("context=%#v err=%v", result, err)
	}
	if len(result.Artifacts) != 0 || len(result.Omissions) != 40 {
		t.Fatalf("irrelevant artifacts were admitted: artifacts=%d omissions=%d", len(result.Artifacts), len(result.Omissions))
	}
}
