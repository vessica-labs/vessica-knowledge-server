package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type SQLStore struct {
	db      *sql.DB
	dialect string
}

func OpenSQLite(path string) (*SQLStore, error) {
	if path == "" {
		path = "knowledge.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &SQLStore{db: db, dialect: "sqlite"}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func OpenPostgres(url string) (*SQLStore, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLStore{db: db, dialect: "postgres"}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *SQLStore) Close() error                   { return s.db.Close() }
func (s *SQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *SQLStore) q(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

const schema = `
CREATE TABLE IF NOT EXISTS knowledge_events(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, aggregate_type TEXT NOT NULL, aggregate_id TEXT NOT NULL, aggregate_version INTEGER NOT NULL, event_type TEXT NOT NULL, actor_json TEXT NOT NULL, provenance_json TEXT NOT NULL, idempotency_key TEXT NOT NULL, payload_json TEXT NOT NULL, occurred_at TEXT NOT NULL, UNIQUE(workspace_id,idempotency_key));
CREATE TABLE IF NOT EXISTS scopes(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, parent_id TEXT, type TEXT NOT NULL, name TEXT NOT NULL, canonical_key TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(workspace_id,canonical_key));
CREATE TABLE IF NOT EXISTS entities(id TEXT NOT NULL, workspace_id TEXT NOT NULL, scope_id TEXT NOT NULL, version INTEGER NOT NULL, type TEXT NOT NULL, display_name TEXT NOT NULL, aliases_json TEXT NOT NULL, external_refs_json TEXT NOT NULL, metadata_json TEXT NOT NULL, state TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,id,version));
CREATE TABLE IF NOT EXISTS entity_current(workspace_id TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(workspace_id,id));
CREATE TABLE IF NOT EXISTS artifacts(id TEXT NOT NULL, workspace_id TEXT NOT NULL, scope_id TEXT NOT NULL, version INTEGER NOT NULL, type TEXT NOT NULL, title TEXT NOT NULL, status TEXT NOT NULL, content TEXT NOT NULL, content_hash TEXT NOT NULL, source_ref_json TEXT, metadata_json TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,id,version));
CREATE TABLE IF NOT EXISTS artifact_current(workspace_id TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(workspace_id,id));
CREATE TABLE IF NOT EXISTS memories(id TEXT NOT NULL, workspace_id TEXT NOT NULL, scope_id TEXT NOT NULL, version INTEGER NOT NULL, type TEXT NOT NULL, subject TEXT, predicate TEXT, object TEXT, title TEXT NOT NULL, content TEXT NOT NULL, importance REAL NOT NULL, confidence REAL NOT NULL, confidence_source TEXT NOT NULL, valid_from TEXT, valid_until TEXT, state TEXT NOT NULL, embedding_state TEXT NOT NULL, metadata_json TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,id,version));
CREATE TABLE IF NOT EXISTS memory_current(workspace_id TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(workspace_id,id));
CREATE TABLE IF NOT EXISTS relationships(id TEXT NOT NULL, workspace_id TEXT NOT NULL, scope_id TEXT NOT NULL, version INTEGER NOT NULL, from_type TEXT NOT NULL, from_id TEXT NOT NULL, predicate TEXT NOT NULL, to_type TEXT NOT NULL, to_id TEXT NOT NULL, confidence REAL NOT NULL, state TEXT NOT NULL, metadata_json TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(workspace_id,id,version));
CREATE TABLE IF NOT EXISTS relationship_current(workspace_id TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(workspace_id,id));
CREATE TABLE IF NOT EXISTS embeddings(workspace_id TEXT NOT NULL, object_type TEXT NOT NULL, object_id TEXT NOT NULL, version INTEGER NOT NULL, vector_json TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(workspace_id,object_type,object_id,version));
CREATE TABLE IF NOT EXISTS embedding_jobs(workspace_id TEXT NOT NULL, memory_id TEXT NOT NULL, version INTEGER NOT NULL, text TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, available_at TEXT NOT NULL, last_error TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,memory_id,version));
CREATE INDEX IF NOT EXISTS idx_events_workspace ON knowledge_events(workspace_id,occurred_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_scope ON artifacts(workspace_id,scope_id,type,status);
CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(workspace_id,scope_id,type,state);
`

const knowledgeMigrationTable = `
CREATE TABLE IF NOT EXISTS knowledge_schema_migrations(
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);`

const latestKnowledgeSchemaVersion = 1

func (s *SQLStore) migrate(ctx context.Context) error {
	if s.dialect == "postgres" {
		var vectorInstalled bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&vectorInstalled); err != nil {
			return fmt.Errorf("inspect pgvector extension: %w", err)
		}
		if !vectorInstalled {
			return fmt.Errorf("pgvector extension is not installed in the knowledge database")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.dialect == "postgres" {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('vessica-knowledge-schema'))`); err != nil {
			return fmt.Errorf("lock knowledge migrations: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, knowledgeMigrationTable); err != nil {
		return fmt.Errorf("create knowledge migration history: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM knowledge_schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read knowledge migration version: %w", err)
	}
	if version > latestKnowledgeSchemaVersion {
		return fmt.Errorf("knowledge schema version %d is newer than supported version %d", version, latestKnowledgeSchemaVersion)
	}
	if version == latestKnowledgeSchemaVersion {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if s.dialect == "postgres" {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE embeddings ADD COLUMN IF NOT EXISTS embedding vector`); err != nil {
			return err
		}
	}
	if s.dialect == "sqlite" {
		if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(workspace_id UNINDEXED,id UNINDEXED,title,content); CREATE VIRTUAL TABLE IF NOT EXISTS entity_fts USING fts5(workspace_id UNINDEXED,id UNINDEXED,display_name,aliases);`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO knowledge_schema_migrations(version,applied_at) VALUES(?,?)`), latestKnowledgeSchemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record knowledge migration: %w", err)
	}
	return tx.Commit()
}

func js(v any) string          { b, _ := json.Marshal(v); return string(b) }
func fromjs(raw string, v any) { _ = json.Unmarshal([]byte(raw), v) }
func timeText(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return nil
	}
	return &t
}

func (s *SQLStore) eventTx(ctx context.Context, e Event, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO knowledge_events(id,workspace_id,aggregate_type,aggregate_id,aggregate_version,event_type,actor_json,provenance_json,idempotency_key,payload_json,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`), e.ID, e.WorkspaceID, e.AggregateType, e.AggregateID, e.AggregateVersion, e.Type, js(e.Actor), js(e.Provenance), e.IdempotencyKey, js(e.Payload), e.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if err = apply(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func upsertCurrent(ctx context.Context, tx *sql.Tx, dialect, table, workspace, id string, version int) error {
	q := `INSERT INTO ` + table + `(workspace_id,id,version) VALUES(?,?,?) ON CONFLICT(workspace_id,id) DO UPDATE SET version=excluded.version`
	if dialect == "postgres" {
		q = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(q, "?", "$1"), "$1", "$2"), "$2", "$3")
	}
	if dialect == "postgres" {
		q = `INSERT INTO ` + table + `(workspace_id,id,version) VALUES($1,$2,$3) ON CONFLICT(workspace_id,id) DO UPDATE SET version=excluded.version`
	}
	_, e := tx.ExecContext(ctx, q, workspace, id, version)
	return e
}

func (s *SQLStore) GetEventByIdempotency(ctx context.Context, w, k string) (*Event, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,workspace_id,aggregate_type,aggregate_id,aggregate_version,event_type,actor_json,provenance_json,idempotency_key,payload_json,occurred_at FROM knowledge_events WHERE workspace_id=? AND idempotency_key=?`), w, k)
	e, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &e, err
}

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (Event, error) {
	var e Event
	var a, p, b, at string
	err := row.Scan(&e.ID, &e.WorkspaceID, &e.AggregateType, &e.AggregateID, &e.AggregateVersion, &e.Type, &a, &p, &e.IdempotencyKey, &b, &at)
	fromjs(a, &e.Actor)
	fromjs(p, &e.Provenance)
	fromjs(b, &e.Payload)
	e.OccurredAt, _ = time.Parse(time.RFC3339Nano, at)
	return e, err
}

func (s *SQLStore) CreateScope(ctx context.Context, v Scope, e Event) (Scope, error) {
	err := s.eventTx(ctx, e, func(tx *sql.Tx) error {
		_, x := tx.ExecContext(ctx, s.q(`INSERT INTO scopes(id,workspace_id,parent_id,type,name,canonical_key,created_at) VALUES(?,?,?,?,?,?,?)`), v.ID, v.WorkspaceID, null(v.ParentID), v.Type, v.Name, v.CanonicalKey, v.CreatedAt.Format(time.RFC3339Nano))
		return x
	})
	return v, err
}
func (s *SQLStore) GetScope(ctx context.Context, w, id string) (Scope, error) {
	var v Scope
	var parent, created string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id,workspace_id,COALESCE(parent_id,''),type,name,canonical_key,created_at FROM scopes WHERE workspace_id=? AND (id=? OR canonical_key=?)`), w, id, id).Scan(&v.ID, &v.WorkspaceID, &parent, &v.Type, &v.Name, &v.CanonicalKey, &created)
	v.ParentID = parent
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err == sql.ErrNoRows {
		return v, NotFound("scope not found")
	}
	return v, err
}
func (s *SQLStore) ScopeAncestors(ctx context.Context, w string, ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids)*2)
	var visit func(string) error
	visit = func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		v, err := s.GetScope(ctx, w, id)
		if err != nil {
			return err
		}
		if err := visit(v.ParentID); err != nil {
			return err
		}
		seen[v.ID] = true
		out = append(out, v.ID)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLStore) PutEntity(ctx context.Context, v Entity, e Event) (Entity, error) {
	err := s.eventTx(ctx, e, func(tx *sql.Tx) error {
		_, x := tx.ExecContext(ctx, s.q(`INSERT INTO entities(id,workspace_id,scope_id,version,type,display_name,aliases_json,external_refs_json,metadata_json,state,provenance_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID, v.WorkspaceID, v.ScopeID, v.Version, v.Type, v.DisplayName, js(v.Aliases), js(v.ExternalRefs), js(v.Metadata), v.State, js(v.Provenance), v.CreatedAt.Format(time.RFC3339Nano), v.UpdatedAt.Format(time.RFC3339Nano))
		if x != nil {
			return x
		}
		return upsertCurrent(ctx, tx, s.dialect, "entity_current", v.WorkspaceID, v.ID, v.Version)
	})
	if err == nil && s.dialect == "sqlite" {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM entity_fts WHERE workspace_id=? AND id=?`, v.WorkspaceID, v.ID)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO entity_fts(workspace_id,id,display_name,aliases) VALUES(?,?,?,?)`, v.WorkspaceID, v.ID, v.DisplayName, strings.Join(v.Aliases, " "))
	}
	return v, err
}
func (s *SQLStore) GetEntity(ctx context.Context, w, id string) (Entity, error) {
	q := `SELECT e.id,e.workspace_id,e.scope_id,e.version,e.type,e.display_name,e.aliases_json,e.external_refs_json,e.metadata_json,e.state,e.provenance_json,e.created_at,e.updated_at FROM entities e JOIN entity_current c ON c.workspace_id=e.workspace_id AND c.id=e.id AND c.version=e.version WHERE e.workspace_id=? AND e.id=?`
	return scanEntity(s.db.QueryRowContext(ctx, s.q(q), w, id))
}
func (s *SQLStore) ListEntities(ctx context.Context, w string, scopes []string, typ, state string) ([]Entity, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT e.id,e.workspace_id,e.scope_id,e.version,e.type,e.display_name,e.aliases_json,e.external_refs_json,e.metadata_json,e.state,e.provenance_json,e.created_at,e.updated_at FROM entities e JOIN entity_current c ON c.workspace_id=e.workspace_id AND c.id=e.id AND c.version=e.version WHERE e.workspace_id=? ORDER BY e.updated_at DESC,e.id`), w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		v, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if includes(scopes, v.ScopeID) && (typ == "" || v.Type == typ) && (state == "" || v.State == state) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
func scanEntity(row scanner) (Entity, error) {
	var v Entity
	var a, r, m, p, c, u string
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.Type, &v.DisplayName, &a, &r, &m, &v.State, &p, &c, &u)
	fromjs(a, &v.Aliases)
	fromjs(r, &v.ExternalRefs)
	fromjs(m, &v.Metadata)
	fromjs(p, &v.Provenance)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	if err == sql.ErrNoRows {
		return v, NotFound("entity not found")
	}
	return v, err
}
func (s *SQLStore) ResolveEntities(ctx context.Context, w string, scopes []string, query string) ([]Entity, error) {
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT e.id,e.workspace_id,e.scope_id,e.version,e.type,e.display_name,e.aliases_json,e.external_refs_json,e.metadata_json,e.state,e.provenance_json,e.created_at,e.updated_at FROM entities e JOIN entity_current c ON c.workspace_id=e.workspace_id AND c.id=e.id AND c.version=e.version WHERE e.workspace_id=? AND (LOWER(e.display_name) LIKE LOWER(?) OR LOWER(e.aliases_json) LIKE LOWER(?)) ORDER BY e.updated_at DESC LIMIT 50`), w, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		v, e := scanEntity(rows)
		if e != nil {
			return nil, e
		}
		if includes(scopes, v.ScopeID) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

func (s *SQLStore) PutArtifact(ctx context.Context, v Artifact, e Event) (Artifact, error) {
	err := s.eventTx(ctx, e, func(tx *sql.Tx) error {
		_, x := tx.ExecContext(ctx, s.q(`INSERT INTO artifacts(id,workspace_id,scope_id,version,type,title,status,content,content_hash,source_ref_json,metadata_json,provenance_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID, v.WorkspaceID, v.ScopeID, v.Version, v.Type, v.Title, v.Status, v.Content, v.ContentHash, js(v.SourceRef), js(v.Metadata), js(v.Provenance), v.CreatedAt.Format(time.RFC3339Nano), v.UpdatedAt.Format(time.RFC3339Nano))
		if x != nil {
			return x
		}
		return upsertCurrent(ctx, tx, s.dialect, "artifact_current", v.WorkspaceID, v.ID, v.Version)
	})
	return v, err
}
func (s *SQLStore) GetArtifact(ctx context.Context, w, id string, version int) (Artifact, error) {
	args := []any{w, id}
	q := `SELECT a.id,a.workspace_id,a.scope_id,a.version,a.type,a.title,a.status,a.content,a.content_hash,COALESCE(a.source_ref_json,''),a.metadata_json,a.provenance_json,a.created_at,a.updated_at FROM artifacts a`
	if version <= 0 {
		q += ` JOIN artifact_current c ON c.workspace_id=a.workspace_id AND c.id=a.id AND c.version=a.version WHERE a.workspace_id=? AND a.id=?`
	} else {
		q += ` WHERE a.workspace_id=? AND a.id=? AND a.version=?`
		args = append(args, version)
	}
	return scanArtifact(s.db.QueryRowContext(ctx, s.q(q), args...))
}
func (s *SQLStore) ListArtifactVersions(ctx context.Context, w, id string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT a.id,a.workspace_id,a.scope_id,a.version,a.type,a.title,a.status,a.content,a.content_hash,COALESCE(a.source_ref_json,''),a.metadata_json,a.provenance_json,a.created_at,a.updated_at FROM artifacts a WHERE a.workspace_id=? AND a.id=? ORDER BY a.version DESC`), w, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		v, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func scanArtifact(row scanner) (Artifact, error) {
	var v Artifact
	var sr, m, p, c, u string
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.Type, &v.Title, &v.Status, &v.Content, &v.ContentHash, &sr, &m, &p, &c, &u)
	if sr != "" && sr != "null" {
		v.SourceRef = &ExternalRef{}
		fromjs(sr, v.SourceRef)
	}
	fromjs(m, &v.Metadata)
	fromjs(p, &v.Provenance)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	if err == sql.ErrNoRows {
		return v, NotFound("artifact not found")
	}
	return v, err
}
func (s *SQLStore) ListArtifacts(ctx context.Context, w string, scopes []string, selectors []ArtifactSelector) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT a.id,a.workspace_id,a.scope_id,a.version,a.type,a.title,a.status,a.content,a.content_hash,COALESCE(a.source_ref_json,''),a.metadata_json,a.provenance_json,a.created_at,a.updated_at FROM artifacts a JOIN artifact_current c ON c.workspace_id=a.workspace_id AND c.id=a.id AND c.version=a.version WHERE a.workspace_id=? ORDER BY a.updated_at DESC`), w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		v, e := scanArtifact(rows)
		if e != nil {
			return nil, e
		}
		if includes(scopes, v.ScopeID) && matchesArtifact(v, selectors) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

func (s *SQLStore) PutMemory(ctx context.Context, v Memory, e Event) (Memory, error) {
	err := s.eventTx(ctx, e, func(tx *sql.Tx) error {
		_, x := tx.ExecContext(ctx, s.q(`INSERT INTO memories(id,workspace_id,scope_id,version,type,subject,predicate,object,title,content,importance,confidence,confidence_source,valid_from,valid_until,state,embedding_state,metadata_json,provenance_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID, v.WorkspaceID, v.ScopeID, v.Version, v.Type, null(v.Subject), null(v.Predicate), null(v.Object), v.Title, v.Content, v.Importance, v.Confidence, v.ConfidenceSource, timeText(v.ValidFrom), timeText(v.ValidUntil), v.State, v.EmbeddingState, js(v.Metadata), js(v.Provenance), v.CreatedAt.Format(time.RFC3339Nano), v.UpdatedAt.Format(time.RFC3339Nano))
		if x != nil {
			return x
		}
		if x = upsertCurrent(ctx, tx, s.dialect, "memory_current", v.WorkspaceID, v.ID, v.Version); x != nil {
			return x
		}
		if v.EmbeddingState == "pending" {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, x = tx.ExecContext(ctx, s.q(`INSERT INTO embedding_jobs(workspace_id,memory_id,version,text,status,attempts,available_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,memory_id,version) DO UPDATE SET text=excluded.text,status=CASE WHEN embedding_jobs.status='completed' THEN embedding_jobs.status ELSE 'pending' END,available_at=excluded.available_at,updated_at=excluded.updated_at`), v.WorkspaceID, v.ID, v.Version, v.Title+"\n"+v.Content, "pending", 0, now, now)
		}
		return x
	})
	if err == nil && s.dialect == "sqlite" {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE workspace_id=? AND id=?`, v.WorkspaceID, v.ID)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO memory_fts(workspace_id,id,title,content) VALUES(?,?,?,?)`, v.WorkspaceID, v.ID, v.Title, v.Content)
	}
	return v, err
}

func (s *SQLStore) EnqueueEmbedding(ctx context.Context, w, id string, version int, text string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO embedding_jobs(workspace_id,memory_id,version,text,status,attempts,available_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,memory_id,version) DO UPDATE SET text=excluded.text,status=CASE WHEN embedding_jobs.status='completed' THEN embedding_jobs.status ELSE 'pending' END,available_at=excluded.available_at,updated_at=excluded.updated_at`), w, id, version, text, "pending", 0, now, now)
	return err
}
func (s *SQLStore) ClaimEmbedding(ctx context.Context) (*EmbeddingJob, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := s.db.QueryRowContext(ctx, s.q(`SELECT workspace_id,memory_id,version,text,attempts FROM embedding_jobs WHERE status IN ('pending','retry') AND available_at<=? ORDER BY updated_at LIMIT 1`), now)
	var j EmbeddingJob
	if err := row.Scan(&j.WorkspaceID, &j.MemoryID, &j.Version, &j.Text, &j.Attempts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE embedding_jobs SET status='running',attempts=attempts+1,updated_at=? WHERE workspace_id=? AND memory_id=? AND version=? AND status IN ('pending','retry')`), now, j.WorkspaceID, j.MemoryID, j.Version)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, nil
	}
	j.Attempts++
	return &j, nil
}
func (s *SQLStore) FinishEmbedding(ctx context.Context, j EmbeddingJob, failure error) error {
	now := time.Now().UTC()
	if failure == nil {
		_, err := s.db.ExecContext(ctx, s.q(`UPDATE embedding_jobs SET status='completed',last_error=NULL,updated_at=? WHERE workspace_id=? AND memory_id=? AND version=?`), now.Format(time.RFC3339Nano), j.WorkspaceID, j.MemoryID, j.Version)
		return err
	}
	status := "retry"
	if j.Attempts >= 5 {
		status = "failed"
	}
	available := now.Add(time.Duration(j.Attempts*j.Attempts) * time.Second).Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE embedding_jobs SET status=?,last_error=?,available_at=?,updated_at=? WHERE workspace_id=? AND memory_id=? AND version=?`), status, failure.Error(), available, now.Format(time.RFC3339Nano), j.WorkspaceID, j.MemoryID, j.Version)
	return err
}
func (s *SQLStore) EmbeddingBacklog(ctx context.Context, workspace string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM embedding_jobs WHERE workspace_id=? AND status IN ('pending','retry','running')`), workspace).Scan(&n)
	return n, err
}

// QueueEmbeddings schedules current, active memory versions for asynchronous
// embedding. The operation is intentionally provider agnostic: workers always
// use the currently configured embedder, and PutEmbedding replaces a prior
// provider/model representation for the same memory version.
func (s *SQLStore) QueueEmbeddings(ctx context.Context, workspace, mode string) (int, error) {
	if mode == "" {
		mode = "missing"
	}
	if mode != "missing" && mode != "all" {
		return 0, Invalid("embedding backfill mode must be missing or all")
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT m.id,m.version,m.title,m.content,m.embedding_state,COALESCE(e.object_id,'')
		FROM memories m
		JOIN memory_current c ON c.workspace_id=m.workspace_id AND c.id=m.id AND c.version=m.version
		LEFT JOIN embeddings e ON e.workspace_id=m.workspace_id AND e.object_type='memory' AND e.object_id=m.id AND e.version=m.version
		WHERE m.workspace_id=? AND m.state NOT IN ('archived','tombstoned')
		ORDER BY m.updated_at,m.id`), workspace)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id, title, content, state, embedded string
		version                             int
	}
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err := rows.Scan(&v.id, &v.version, &v.title, &v.content, &v.state, &v.embedded); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if mode == "all" || v.embedded == "" || v.state == "not_configured" || v.state == "failed" {
			candidates = append(candidates, v)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, v := range candidates {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO embedding_jobs(workspace_id,memory_id,version,text,status,attempts,available_at,last_error,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id,memory_id,version) DO UPDATE SET text=excluded.text,status='pending',attempts=0,available_at=excluded.available_at,last_error=NULL,updated_at=excluded.updated_at`),
			workspace, v.id, v.version, v.title+"\n"+v.content, "pending", 0, now, nil, now); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE memories SET embedding_state='pending' WHERE workspace_id=? AND id=? AND version=?`), workspace, v.id, v.version); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(candidates), nil
}
func (s *SQLStore) GetMemory(ctx context.Context, w, id string, version int) (Memory, error) {
	args := []any{w, id}
	q := `SELECT m.id,m.workspace_id,m.scope_id,m.version,m.type,COALESCE(m.subject,''),COALESCE(m.predicate,''),COALESCE(m.object,''),m.title,m.content,m.importance,m.confidence,m.confidence_source,COALESCE(m.valid_from,''),COALESCE(m.valid_until,''),m.state,m.embedding_state,m.metadata_json,m.provenance_json,m.created_at,m.updated_at FROM memories m`
	if version <= 0 {
		q += ` JOIN memory_current c ON c.workspace_id=m.workspace_id AND c.id=m.id AND c.version=m.version WHERE m.workspace_id=? AND m.id=?`
	} else {
		q += ` WHERE m.workspace_id=? AND m.id=? AND m.version=?`
		args = append(args, version)
	}
	return scanMemory(s.db.QueryRowContext(ctx, s.q(q), args...))
}
func (s *SQLStore) ListMemoryVersions(ctx context.Context, w, id string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT m.id,m.workspace_id,m.scope_id,m.version,m.type,COALESCE(m.subject,''),COALESCE(m.predicate,''),COALESCE(m.object,''),m.title,m.content,m.importance,m.confidence,m.confidence_source,COALESCE(m.valid_from,''),COALESCE(m.valid_until,''),m.state,m.embedding_state,m.metadata_json,m.provenance_json,m.created_at,m.updated_at FROM memories m WHERE m.workspace_id=? AND m.id=? ORDER BY m.version DESC`), w, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		v, scanErr := scanMemory(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func scanMemory(row scanner) (Memory, error) {
	var v Memory
	var vf, vu, md, p, c, u string
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.Type, &v.Subject, &v.Predicate, &v.Object, &v.Title, &v.Content, &v.Importance, &v.Confidence, &v.ConfidenceSource, &vf, &vu, &v.State, &v.EmbeddingState, &md, &p, &c, &u)
	v.ValidFrom = parseTime(vf)
	v.ValidUntil = parseTime(vu)
	fromjs(md, &v.Metadata)
	fromjs(p, &v.Provenance)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	if err == sql.ErrNoRows {
		return v, NotFound("memory not found")
	}
	return v, err
}
func (s *SQLStore) SearchMemories(ctx context.Context, w string, scopes []string, query string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	term := strings.TrimSpace(query)
	if s.dialect == "sqlite" && term != "" {
		// Quote user terms so FTS operators are never interpreted from prompts.
		parts := strings.Fields(term)
		for i := range parts {
			parts[i] = `"` + strings.ReplaceAll(parts[i], `"`, `""`) + `"*`
		}
		match := strings.Join(parts, " OR ")
		rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.workspace_id,m.scope_id,m.version,m.type,COALESCE(m.subject,''),COALESCE(m.predicate,''),COALESCE(m.object,''),m.title,m.content,m.importance,m.confidence,m.confidence_source,COALESCE(m.valid_from,''),COALESCE(m.valid_until,''),m.state,m.embedding_state,m.metadata_json,m.provenance_json,m.created_at,m.updated_at FROM memory_fts f JOIN memory_current c ON c.workspace_id=f.workspace_id AND c.id=f.id JOIN memories m ON m.workspace_id=c.workspace_id AND m.id=c.id AND m.version=c.version WHERE f.workspace_id=? AND memory_fts MATCH ? AND m.state NOT IN ('archived','tombstoned') ORDER BY bm25(memory_fts),m.updated_at DESC LIMIT ?`, w, match, limit)
		if err == nil {
			defer rows.Close()
			var out []Memory
			for rows.Next() {
				v, e := scanMemory(rows)
				if e != nil {
					return nil, e
				}
				if includes(scopes, v.ScopeID) {
					out = append(out, v)
				}
			}
			return out, rows.Err()
		}
	}
	if fields := strings.Fields(term); len(fields) > 0 {
		term = fields[0]
	}
	like := "%" + term + "%"
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT m.id,m.workspace_id,m.scope_id,m.version,m.type,COALESCE(m.subject,''),COALESCE(m.predicate,''),COALESCE(m.object,''),m.title,m.content,m.importance,m.confidence,m.confidence_source,COALESCE(m.valid_from,''),COALESCE(m.valid_until,''),m.state,m.embedding_state,m.metadata_json,m.provenance_json,m.created_at,m.updated_at FROM memories m JOIN memory_current c ON c.workspace_id=m.workspace_id AND c.id=m.id AND c.version=m.version WHERE m.workspace_id=? AND m.state NOT IN ('archived','tombstoned') AND (LOWER(m.title) LIKE LOWER(?) OR LOWER(m.content) LIKE LOWER(?)) ORDER BY m.updated_at DESC LIMIT ?`), w, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		v, e := scanMemory(rows)
		if e != nil {
			return nil, e
		}
		if includes(scopes, v.ScopeID) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

func (s *SQLStore) PutRelationship(ctx context.Context, v Relationship, e Event) (Relationship, error) {
	err := s.eventTx(ctx, e, func(tx *sql.Tx) error {
		_, x := tx.ExecContext(ctx, s.q(`INSERT INTO relationships(id,workspace_id,scope_id,version,from_type,from_id,predicate,to_type,to_id,confidence,state,metadata_json,provenance_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID, v.WorkspaceID, v.ScopeID, v.Version, v.FromType, v.FromID, v.Predicate, v.ToType, v.ToID, v.Confidence, v.State, js(v.Metadata), js(v.Provenance), v.CreatedAt.Format(time.RFC3339Nano))
		if x != nil {
			return x
		}
		return upsertCurrent(ctx, tx, s.dialect, "relationship_current", v.WorkspaceID, v.ID, v.Version)
	})
	return v, err
}
func (s *SQLStore) GetRelationship(ctx context.Context, w, id string) (Relationship, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT r.id,r.workspace_id,r.scope_id,r.version,r.from_type,r.from_id,r.predicate,r.to_type,r.to_id,r.confidence,r.state,r.metadata_json,r.provenance_json,r.created_at FROM relationships r JOIN relationship_current c ON c.workspace_id=r.workspace_id AND c.id=r.id AND c.version=r.version WHERE r.workspace_id=? AND r.id=?`), w, id)
	var v Relationship
	var md, p, c string
	if err := row.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.FromType, &v.FromID, &v.Predicate, &v.ToType, &v.ToID, &v.Confidence, &v.State, &md, &p, &c); err != nil {
		if err == sql.ErrNoRows {
			return v, NotFound("relationship not found")
		}
		return v, err
	}
	fromjs(md, &v.Metadata)
	fromjs(p, &v.Provenance)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	return v, nil
}
func (s *SQLStore) ListRelationships(ctx context.Context, w, objectID string) ([]Relationship, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT r.id,r.workspace_id,r.scope_id,r.version,r.from_type,r.from_id,r.predicate,r.to_type,r.to_id,r.confidence,r.state,r.metadata_json,r.provenance_json,r.created_at FROM relationships r JOIN relationship_current c ON c.workspace_id=r.workspace_id AND c.id=r.id AND c.version=r.version WHERE r.workspace_id=? AND (r.from_id=? OR r.to_id=?)`), w, objectID, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relationship
	for rows.Next() {
		var v Relationship
		var md, p, c string
		if e := rows.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.FromType, &v.FromID, &v.Predicate, &v.ToType, &v.ToID, &v.Confidence, &v.State, &md, &p, &c); e != nil {
			return nil, e
		}
		fromjs(md, &v.Metadata)
		fromjs(p, &v.Provenance)
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQLStore) ListEvents(ctx context.Context, w string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,aggregate_type,aggregate_id,aggregate_version,event_type,actor_json,provenance_json,idempotency_key,payload_json,occurred_at FROM knowledge_events WHERE workspace_id=? ORDER BY occurred_at,id`), w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, x := scanEvent(rows)
		if x != nil {
			return nil, x
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLStore) PutEmbedding(ctx context.Context, w, objectType, objectID string, version int, vec []float32, provider, model string) error {
	if s.dialect == "postgres" {
		_, err := s.db.ExecContext(ctx, `INSERT INTO embeddings(workspace_id,object_type,object_id,version,vector_json,provider,model,created_at,embedding) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::vector) ON CONFLICT(workspace_id,object_type,object_id,version) DO UPDATE SET vector_json=excluded.vector_json,provider=excluded.provider,model=excluded.model,created_at=excluded.created_at,embedding=excluded.embedding`, w, objectType, objectID, version, js(vec), provider, model, time.Now().UTC().Format(time.RFC3339Nano), vectorText(vec))
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO embeddings(workspace_id,object_type,object_id,version,vector_json,provider,model,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,object_type,object_id,version) DO UPDATE SET vector_json=excluded.vector_json,provider=excluded.provider,model=excluded.model,created_at=excluded.created_at`), w, objectType, objectID, version, js(vec), provider, model, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func (s *SQLStore) SetEmbeddingState(ctx context.Context, w, id string, version int, state string) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE memories SET embedding_state=? WHERE workspace_id=? AND id=? AND version=?`), state, w, id, version)
	return err
}
func (s *SQLStore) SearchEmbeddings(ctx context.Context, w string, scopes []string, query []float32, limit int) (map[string]float64, error) {
	if s.dialect == "postgres" {
		if limit <= 0 {
			limit = 200
		}
		rows, err := s.db.QueryContext(ctx, `SELECT e.object_id,1-(e.embedding <=> $1::vector) FROM embeddings e JOIN memories m ON m.workspace_id=e.workspace_id AND m.id=e.object_id AND m.version=e.version WHERE e.workspace_id=$2 AND e.object_type='memory' AND e.embedding IS NOT NULL AND m.embedding_state='ready' ORDER BY e.embedding <=> $1::vector LIMIT $3`, vectorText(query), w, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]float64{}
		for rows.Next() {
			var id string
			var score float64
			if err := rows.Scan(&id, &score); err != nil {
				return nil, err
			}
			out[id] = score
		}
		return out, rows.Err()
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT e.object_id,e.vector_json FROM embeddings e JOIN memories m ON m.workspace_id=e.workspace_id AND m.id=e.object_id AND m.version=e.version WHERE e.workspace_id=? AND e.object_type='memory' AND m.embedding_state='ready'`), w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var v []float32
		fromjs(raw, &v)
		out[id] = cosine(query, v)
	}
	return out, rows.Err()
}

func (s *SQLStore) Export(ctx context.Context, w string) (Snapshot, error) {
	events, err := s.ListEvents(ctx, w)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Schema: APIVersion, WorkspaceID: w, ExportedAt: time.Now().UTC(), Events: events}
	scopes, entities, arts, mems, rels, err := s.all(ctx, w)
	if err != nil {
		return snap, err
	}
	snap.Scopes = scopes
	snap.Entities = entities
	snap.Artifacts = arts
	snap.Memories = mems
	snap.Relationships = rels
	snap.Counts = map[string]int{"scopes": len(scopes), "entities": len(entities), "artifacts": len(arts), "memories": len(mems), "relationships": len(rels), "events": len(events)}
	if len(events) > 0 {
		snap.HighWatermark = events[len(events)-1].ID
	}
	snap.Checksum = snapshotChecksum(snap)
	return snap, nil
}
func (s *SQLStore) Import(ctx context.Context, snap Snapshot) error {
	if snap.Schema != APIVersion {
		return Invalid("unsupported snapshot schema")
	}
	if snapshotChecksum(snap) != snap.Checksum {
		return Invalid("snapshot checksum mismatch")
	}
	for _, e := range snap.Events {
		if old, _ := s.GetEventByIdempotency(ctx, e.WorkspaceID, e.IdempotencyKey); old != nil {
			continue
		}
		switch e.AggregateType {
		case "scope":
			v := findScope(snap.Scopes, e.AggregateID)
			if _, err := s.CreateScope(ctx, v, e); err != nil {
				return err
			}
		case "entity":
			v := findEntity(snap.Entities, e.AggregateID, e.AggregateVersion)
			if _, err := s.PutEntity(ctx, v, e); err != nil {
				return err
			}
		case "artifact":
			v := findArtifact(snap.Artifacts, e.AggregateID, e.AggregateVersion)
			if _, err := s.PutArtifact(ctx, v, e); err != nil {
				return err
			}
		case "memory":
			v := findMemory(snap.Memories, e.AggregateID, e.AggregateVersion)
			if _, err := s.PutMemory(ctx, v, e); err != nil {
				return err
			}
		case "relationship":
			v := findRelationship(snap.Relationships, e.AggregateID, e.AggregateVersion)
			if _, err := s.PutRelationship(ctx, v, e); err != nil {
				return err
			}
		}
	}
	got, err := s.Export(ctx, snap.WorkspaceID)
	if err != nil {
		return err
	}
	for k, n := range snap.Counts {
		if got.Counts[k] != n {
			return fmt.Errorf("import count mismatch for %s: %d != %d", k, got.Counts[k], n)
		}
	}
	return nil
}
func (s *SQLStore) RebuildProjections(ctx context.Context, w string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, pair := range [][2]string{{"entity_current", "entities"}, {"artifact_current", "artifacts"}, {"memory_current", "memories"}, {"relationship_current", "relationships"}} {
		if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM `+pair[0]+` WHERE workspace_id=?`), w); err != nil {
			return err
		}
		q := `INSERT INTO ` + pair[0] + `(workspace_id,id,version) SELECT workspace_id,id,MAX(version) FROM ` + pair[1] + ` WHERE workspace_id=? GROUP BY workspace_id,id`
		if _, err = tx.ExecContext(ctx, s.q(q), w); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.dialect == "sqlite" {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE workspace_id=?`, w)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO memory_fts(workspace_id,id,title,content) SELECT m.workspace_id,m.id,m.title,m.content FROM memories m JOIN memory_current c ON c.workspace_id=m.workspace_id AND c.id=m.id AND c.version=m.version WHERE m.workspace_id=?`, w)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM entity_fts WHERE workspace_id=?`, w)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO entity_fts(workspace_id,id,display_name,aliases) SELECT e.workspace_id,e.id,e.display_name,e.aliases_json FROM entities e JOIN entity_current c ON c.workspace_id=e.workspace_id AND c.id=e.id AND c.version=e.version WHERE e.workspace_id=?`, w)
	}
	return nil
}

func (s *SQLStore) all(ctx context.Context, w string) ([]Scope, []Entity, []Artifact, []Memory, []Relationship, error) {
	var scopes []Scope
	rows, e := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,COALESCE(parent_id,''),type,name,canonical_key,created_at FROM scopes WHERE workspace_id=? ORDER BY id`), w)
	if e != nil {
		return nil, nil, nil, nil, nil, e
	}
	for rows.Next() {
		var v Scope
		var c string
		if e = rows.Scan(&v.ID, &v.WorkspaceID, &v.ParentID, &v.Type, &v.Name, &v.CanonicalKey, &c); e != nil {
			break
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		scopes = append(scopes, v)
	}
	rows.Close()
	if e != nil {
		return nil, nil, nil, nil, nil, e
	}
	entities, e := s.allEntities(ctx, w)
	if e != nil {
		return nil, nil, nil, nil, nil, e
	}
	arts, e := s.allArtifacts(ctx, w)
	if e != nil {
		return nil, nil, nil, nil, nil, e
	}
	mems, e := s.allMemories(ctx, w)
	if e != nil {
		return nil, nil, nil, nil, nil, e
	}
	rels, e := s.allRelationships(ctx, w)
	return scopes, entities, arts, mems, rels, e
}
func (s *SQLStore) allEntities(ctx context.Context, w string) ([]Entity, error) {
	rows, e := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,scope_id,version,type,display_name,aliases_json,external_refs_json,metadata_json,state,provenance_json,created_at,updated_at FROM entities WHERE workspace_id=? ORDER BY id,version`), w)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		v, e := scanEntity(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLStore) allArtifacts(ctx context.Context, w string) ([]Artifact, error) {
	rows, e := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,scope_id,version,type,title,status,content,content_hash,COALESCE(source_ref_json,''),metadata_json,provenance_json,created_at,updated_at FROM artifacts WHERE workspace_id=? ORDER BY id,version`), w)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		v, e := scanArtifact(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLStore) allMemories(ctx context.Context, w string) ([]Memory, error) {
	rows, e := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,scope_id,version,type,COALESCE(subject,''),COALESCE(predicate,''),COALESCE(object,''),title,content,importance,confidence,confidence_source,COALESCE(valid_from,''),COALESCE(valid_until,''),state,embedding_state,metadata_json,provenance_json,created_at,updated_at FROM memories WHERE workspace_id=? ORDER BY id,version`), w)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		v, e := scanMemory(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLStore) allRelationships(ctx context.Context, w string) ([]Relationship, error) {
	rows, e := s.db.QueryContext(ctx, s.q(`SELECT id,workspace_id,scope_id,version,from_type,from_id,predicate,to_type,to_id,confidence,state,metadata_json,provenance_json,created_at FROM relationships WHERE workspace_id=? ORDER BY id,version`), w)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Relationship
	for rows.Next() {
		var v Relationship
		var md, p, c string
		if e := rows.Scan(&v.ID, &v.WorkspaceID, &v.ScopeID, &v.Version, &v.FromType, &v.FromID, &v.Predicate, &v.ToType, &v.ToID, &v.Confidence, &v.State, &md, &p, &c); e != nil {
			return nil, e
		}
		fromjs(md, &v.Metadata)
		fromjs(p, &v.Provenance)
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		out = append(out, v)
	}
	return out, rows.Err()
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func vectorText(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprintf("%g", x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
func includes(scopes []string, s string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, x := range scopes {
		if x == s {
			return true
		}
	}
	return false
}
func matchesArtifact(a Artifact, ss []ArtifactSelector) bool {
	if len(ss) == 0 {
		return true
	}
	for _, s := range ss {
		if (s.ID == "" || s.ID == a.ID) && (s.Type == "" || s.Type == a.Type) && (s.Status == "" || s.Status == a.Status) && (s.Version == 0 || s.Version == a.Version) {
			return true
		}
	}
	return false
}
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		aa += float64(a[i] * a[i])
		bb += float64(b[i] * b[i])
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (sqrt(aa) * sqrt(bb))
}
func sqrt(v float64) float64 {
	z := v
	for i := 0; i < 20; i++ {
		if z == 0 {
			return 0
		}
		z = (z + v/z) / 2
	}
	return z
}
func snapshotChecksum(s Snapshot) string {
	s.Checksum = ""
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func findEntity(v []Entity, id string, n int) Entity {
	for _, x := range v {
		if x.ID == id && x.Version == n {
			return x
		}
	}
	return Entity{}
}
func findScope(v []Scope, id string) Scope {
	for _, x := range v {
		if x.ID == id {
			return x
		}
	}
	return Scope{}
}
func findArtifact(v []Artifact, id string, n int) Artifact {
	for _, x := range v {
		if x.ID == id && x.Version == n {
			return x
		}
	}
	return Artifact{}
}
func findMemory(v []Memory, id string, n int) Memory {
	for _, x := range v {
		if x.ID == id && x.Version == n {
			return x
		}
	}
	return Memory{}
}
func findRelationship(v []Relationship, id string, n int) Relationship {
	for _, x := range v {
		if x.ID == id && x.Version == n {
			return x
		}
	}
	return Relationship{}
}

var _ = sort.Strings
