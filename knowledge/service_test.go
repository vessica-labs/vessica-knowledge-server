package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeEmbedder struct{}

func (fakeEmbedder) Provider() string { return "fake" }
func (fakeEmbedder) Model() string    { return "fake-v1" }
func (fakeEmbedder) Embed(_ context.Context, s string) ([]float32, error) {
	return []float32{float32(len(s)%7 + 1), 1, 2}, nil
}

func TestSQLiteKnowledgeContract(t *testing.T) {
	runContract(t, func(t *testing.T) Store {
		s, e := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
		if e != nil {
			t.Fatal(e)
		}
		return s
	})
}
func TestPostgresKnowledgeContract(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	runContract(t, func(t *testing.T) Store {
		s, e := OpenPostgres(url)
		if e != nil {
			t.Fatal(e)
		}
		return s
	})
}

func TestFullReembeddingExcludesStaleVectorsWhileCatchingUp(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := NewID("kwsp")
	service := NewService(store, nil)
	options := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: workspace, IdempotencyKey: key, Actor: Actor{ID: "tester", Type: "human"}, Provenance: Provenance{Source: "test"}}
	}
	scope, err := service.CreateScope(ctx, options("scope"), Scope{Type: "repository", Name: "repo", CanonicalKey: "github.com/acme/repo"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(ctx, options("memory"), Memory{ScopeID: scope.ID, Type: "fact", Title: "Architecture", Content: "The API uses Go."})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutEmbedding(ctx, workspace, "memory", memory.ID, memory.Version, []float32{1, 0, 0}, "old-provider", "old-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbeddingState(ctx, workspace, memory.ID, memory.Version, "ready"); err != nil {
		t.Fatal(err)
	}
	before, err := store.SearchEmbeddings(ctx, workspace, nil, []float32{1, 0, 0}, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("ready vectors=%v err=%v", before, err)
	}
	if queued, err := store.QueueEmbeddings(ctx, workspace, "all"); err != nil || queued != 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
	after, err := store.SearchEmbeddings(ctx, workspace, nil, []float32{1, 0, 0}, 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("stale vectors remained visible=%v err=%v", after, err)
	}
}

func runContract(t *testing.T, open func(*testing.T) Store) {
	t.Helper()
	ctx := context.Background()
	store := open(t)
	defer store.Close()
	w := NewID("kwsp")
	svc := NewService(store, nil)
	actor := Actor{ID: "tester", Type: "human"}
	prov := Provenance{Source: "test"}
	opt := func(key string) WriteOptions {
		return WriteOptions{WorkspaceID: w, IdempotencyKey: key, Actor: actor, Provenance: prov}
	}
	scope, err := svc.CreateScope(ctx, opt("scope"), Scope{Type: "repository", Name: "repo", CanonicalKey: "github.com/vessica/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := svc.CreateScope(ctx, opt("scope"), Scope{Type: "repository", Name: "duplicate", CanonicalKey: "duplicate"}); err != nil || replay.ID != scope.ID {
		t.Fatalf("scope replay=%#v err=%v", replay, err)
	}
	ent, err := svc.CreateEntity(ctx, opt("entity"), Entity{ScopeID: scope.ID, Type: "repository", DisplayName: "Vessica Repo", Aliases: []string{"ves repo"}})
	if err != nil {
		t.Fatal(err)
	}
	art, err := svc.CreateArtifact(ctx, opt("artifact"), Artifact{ScopeID: scope.ID, Type: "prd", Title: "Knowledge PRD", Status: "active", Content: "Use SQLite locally and Postgres in teams."})
	if err != nil {
		t.Fatal(err)
	}
	art2, err := svc.VersionArtifact(ctx, opt("artifact-v2"), art.ID, Artifact{Content: "Use zero-key SQLite locally and pgvector in teams.", Status: "active"})
	if err != nil || art2.Version != 2 || art2.ContentHash == art.ContentHash {
		t.Fatalf("artifact=%#v err=%v", art2, err)
	}
	mem, err := svc.CreateMemory(ctx, opt("memory"), Memory{ScopeID: scope.ID, Type: "decision", Title: "Storage decision", Content: "Use SQLite for solo mode and Postgres for hosted mode.", Importance: .9, Confidence: 1, ConfidenceSource: "human_confirmed"})
	if err != nil || mem.EmbeddingState != "not_configured" {
		t.Fatalf("memory=%#v err=%v", mem, err)
	}
	rel, err := svc.CreateRelationship(ctx, opt("relationship"), Relationship{ScopeID: scope.ID, FromType: "memory", FromID: mem.ID, Predicate: "derived_from", ToType: "artifact", ToID: art.ID})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := svc.CreateRelationship(ctx, opt("relationship"), Relationship{ScopeID: scope.ID, FromType: "memory", FromID: "different", Predicate: "about", ToType: "artifact", ToID: art.ID}); err != nil || replay.ID != rel.ID {
		t.Fatalf("relationship replay=%#v err=%v", replay, err)
	}
	ctxResult, err := svc.Context(ctx, ContextRequest{WorkspaceID: w, Query: "SQLite storage", ScopeIDs: []string{scope.ID}, ArtifactSelectors: []ArtifactSelector{{Type: "prd", Status: "active"}}, TokenBudget: 1000})
	if err != nil || ctxResult.RetrievalMode != "lexical" || len(ctxResult.Artifacts) != 1 || len(ctxResult.Decisions) != 1 {
		t.Fatalf("context=%#v err=%v", ctxResult, err)
	}
	episode, err := svc.IngestWorkflowEvent(ctx, opt("ignored"), WorkflowEvent{ID: "run-1-complete", WorkspaceID: w, RepositoryScopeID: scope.ID, Type: "run.completed", Summary: "Implemented the knowledge layer.", Actor: actor, References: []ExternalRef{{System: "github_pr", ID: "42", URL: "https://github/pr/42"}}})
	if err != nil || episode.Type != "episode" {
		t.Fatalf("episode=%#v err=%v", episode, err)
	}
	if _, err := svc.IngestWorkflowEvent(ctx, opt("ignored2"), WorkflowEvent{ID: "heartbeat", WorkspaceID: w, RepositoryScopeID: scope.ID, Type: "ticket.heartbeat", Summary: "noise", Actor: actor}); err == nil {
		t.Fatal("heartbeat should not create memory")
	}
	snap, err := store.Export(ctx, w)
	if err != nil || snap.Checksum == "" || snap.Counts["events"] < 6 {
		t.Fatalf("snapshot=%#v err=%v", snap, err)
	}
	dest, err := OpenSQLite(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err := dest.Import(ctx, snap); err != nil {
		t.Fatal(err)
	}
	copy, err := dest.Export(ctx, w)
	if err != nil || copy.Counts["events"] != snap.Counts["events"] {
		t.Fatalf("copy=%#v err=%v", copy, err)
	}
	if err := dest.RebuildProjections(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got, err := dest.GetEntity(ctx, w, ent.ID); err != nil || got.ID != ent.ID {
		t.Fatalf("entity after rebuild=%#v err=%v", got, err)
	}
}

func TestEmbeddingWorkerAndHybridContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService(store, fakeEmbedder{})
	svc.StartEmbeddingWorker(ctx)
	w := "kwsp_embed"
	o := WriteOptions{WorkspaceID: w, IdempotencyKey: "scope", Actor: Actor{ID: "a", Type: "human"}, Provenance: Provenance{Source: "test"}}
	scope, err := svc.CreateScope(ctx, o, Scope{Type: "repository", Name: "r", CanonicalKey: "r"})
	if err != nil {
		t.Fatal(err)
	}
	o.IdempotencyKey = "mem"
	m, err := svc.CreateMemory(ctx, o, Memory{ScopeID: scope.ID, Type: "fact", Content: "authentication uses OAuth"})
	if err != nil {
		t.Fatal(err)
	}
	o.IdempotencyKey = "semantic-only"
	semanticOnly, err := svc.CreateMemory(ctx, o, Memory{ScopeID: scope.ID, Type: "fact", Content: "bananas grow in warm climates"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.GetMemory(ctx, w, m.ID, 0)
		if got.EmbeddingState == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.GetMemory(ctx, w, semanticOnly.ID, 0)
		if got.EmbeddingState == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := svc.Context(ctx, ContextRequest{WorkspaceID: w, Query: "OAuth", ScopeIDs: []string{scope.ID}})
	if err != nil || result.RetrievalMode != "semantic_hybrid" || result.EmbeddingModel != "fake-v1" {
		t.Fatalf("context=%#v err=%v", result, err)
	}
	if result.Ranking.Version != "v1" || result.Ranking.MemoryWeights["semantic"] == 0 {
		t.Fatalf("ranking=%#v", result.Ranking)
	}
	foundSemanticOnly := false
	for _, fact := range result.Facts {
		if fact.Memory.ID == semanticOnly.ID {
			foundSemanticOnly = true
		}
	}
	if !foundSemanticOnly {
		t.Fatalf("semantic-only memory missing: %#v", result.Facts)
	}
}

func TestEventReplayReconstructsEquivalentProjections(t *testing.T) {
	ctx := context.Background()
	source, err := OpenSQLite(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	svc := NewService(source, nil)
	o := WriteOptions{WorkspaceID: "w1", IdempotencyKey: "scope", Actor: Actor{ID: "u", Type: "user"}, Provenance: Provenance{Source: "test"}}
	scope, err := svc.CreateScope(ctx, o, Scope{Type: "repository", Name: "repo", CanonicalKey: "repo:x"})
	if err != nil {
		t.Fatal(err)
	}
	o.IdempotencyKey = "memory"
	if _, err := svc.CreateMemory(ctx, o, Memory{ScopeID: scope.ID, Type: "fact", Title: "fact", Content: "event replay works"}); err != nil {
		t.Fatal(err)
	}
	want, err := source.Export(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	dest, err := OpenSQLite(filepath.Join(t.TempDir(), "dest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err := ReplayEvents(ctx, want.Events, dest); err != nil {
		t.Fatal(err)
	}
	got, err := dest.Export(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scopes", "memories", "events"} {
		if got.Counts[key] != want.Counts[key] {
			t.Fatalf("%s count mismatch: got %d want %d", key, got.Counts[key], want.Counts[key])
		}
	}
}

func TestScopeHierarchyAndInstructionOverride(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "scopes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService(store, nil)
	base := WriteOptions{WorkspaceID: "w", Actor: Actor{ID: "u", Type: "human"}, Provenance: Provenance{Source: "test"}}
	base.IdempotencyKey = "workspace"
	parent, err := svc.CreateScope(ctx, base, Scope{Type: "workspace", Name: "workspace", CanonicalKey: "workspace:w"})
	if err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "repo"
	repo, err := svc.CreateScope(ctx, base, Scope{ParentID: parent.ID, Type: "repository", Name: "repo", CanonicalKey: "repo:r"})
	if err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "broad"
	if _, err := svc.CreateMemory(ctx, base, Memory{ScopeID: parent.ID, Type: "instruction", Content: "Use broad checks", Metadata: map[string]any{"semantic_key": "tests"}}); err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "specific"
	specific, err := svc.CreateMemory(ctx, base, Memory{ScopeID: repo.ID, Type: "instruction", Content: "Use focused checks", Metadata: map[string]any{"semantic_key": "tests"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Context(ctx, ContextRequest{WorkspaceID: "w", Query: "checks", ScopeIDs: []string{repo.ID}, TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instructions) != 1 || got.Instructions[0].Memory.ID != specific.ID {
		t.Fatalf("instructions=%#v", got.Instructions)
	}
}
