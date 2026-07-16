package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeMigrationHistoryIsSeparateAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.db")
	for attempt := 0; attempt < 2; attempt++ {
		store, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := store.db.QueryRowContext(context.Background(), `SELECT MAX(version) FROM knowledge_schema_migrations`).Scan(&version); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if version != latestKnowledgeSchemaVersion {
			store.Close()
			t.Fatalf("version=%d want=%d", version, latestKnowledgeSchemaVersion)
		}
		var controlHistory int
		if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&controlHistory); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if controlHistory != 0 {
			store.Close()
			t.Fatal("knowledge store must not share the control-plane migration history")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresKnowledgeMigrationHistoryAndVector(t *testing.T) {
	rawURL := os.Getenv("TEST_POSTGRES_URL")
	if rawURL == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	store, err := OpenPostgres(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	var version int
	if err := store.db.QueryRowContext(ctx, `SELECT MAX(version) FROM knowledge_schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latestKnowledgeSchemaVersion {
		t.Fatalf("version=%d want=%d", version, latestKnowledgeSchemaVersion)
	}
	var vectorInstalled bool
	if err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&vectorInstalled); err != nil {
		t.Fatal(err)
	}
	if !vectorInstalled {
		t.Fatal("pgvector must be installed in the knowledge database")
	}
	var controlHistory bool
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&controlHistory); err != nil {
		t.Fatal(err)
	}
	if controlHistory {
		t.Fatal("knowledge database must not contain the control-plane migration history")
	}
}
