# Vessica Knowledge Server

Durable entities, authoritative artifacts, retrieval-optimized memories, relationships, context assembly, workflow episodes, and verified local-to-hosted migration for Vessica agents.

The repository publishes an embeddable Go core used by `ves` in SQLite solo mode and an authenticated HTTP service backed by Postgres/pgvector for hosted workspaces.

## What is included

- Append-only events and replayable projections.
- Immutable entity, artifact, memory, and relationship assertions with provenance.
- SQLite FTS5/BM25 solo retrieval without an external key.
- Postgres/pgvector semantic-hybrid retrieval with durable embedding jobs.
- Versioned ranking metadata, per-memory component scores, and artifact selection reasons.
- Authenticated workspace-scoped HTTP endpoints and separate snapshot authorization.
- Checksummed idempotent export/import used by verified `ves railway up` promotion.

```bash
go test ./...
KNOWLEDGE_SQLITE_PATH=/tmp/knowledge.db go run ./cmd/vessica-knowledge-server
```

Environment variables:

- `DATABASE_URL`: Postgres connection string; omit for SQLite.
- `KNOWLEDGE_SQLITE_PATH`: SQLite path when `DATABASE_URL` is absent.
- `KNOWLEDGE_API_TOKEN`: required bearer token for hosted HTTP requests.
- `KNOWLEDGE_EXPORT_TOKEN`: separately scoped token for snapshot export/import.
- `KNOWLEDGE_WORKSPACE_ID`: default workspace.
- `EMBEDDING_API_KEY`, `EMBEDDING_BASE_URL`, `EMBEDDING_MODEL`: hosted embedding provider.
- `PORT`: listen port, default `8080`.

The complete versioned HTTP contract is documented in [`openapi.yaml`](openapi.yaml). Every write uses an idempotency key, actor, provenance, and a bearer token constrained to `KNOWLEDGE_WORKSPACE_ID`. `/readyz` reports database, migration, embedding-worker, and index readiness separately.

Production images are published as `ghcr.io/vessica-labs/vessica-knowledge-server:<version>` and deployed by immutable digest. Solo callers embed the same core and require no service, model download, or API key.

Read [`docs/OPERATIONS.md`](docs/OPERATIONS.md) for deployment, secrets, readiness, retrieval, promotion, recovery, troubleshooting, and release validation. Normal developers should operate the system through `ves`, not the HTTP API.
