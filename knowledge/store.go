package knowledge

import "context"

type Store interface {
	Close() error
	Ping(context.Context) error
	CreateScope(context.Context, Scope, Event) (Scope, error)
	GetScope(context.Context, string, string) (Scope, error)
	ScopeAncestors(context.Context, string, []string) ([]string, error)
	PutEntity(context.Context, Entity, Event) (Entity, error)
	GetEntity(context.Context, string, string) (Entity, error)
	ListEntities(context.Context, string, []string, string, string) ([]Entity, error)
	ResolveEntities(context.Context, string, []string, string) ([]Entity, error)
	PutArtifact(context.Context, Artifact, Event) (Artifact, error)
	GetArtifact(context.Context, string, string, int) (Artifact, error)
	ListArtifactVersions(context.Context, string, string) ([]Artifact, error)
	ListArtifacts(context.Context, string, []string, []ArtifactSelector) ([]Artifact, error)
	PutMemory(context.Context, Memory, Event) (Memory, error)
	GetMemory(context.Context, string, string, int) (Memory, error)
	ListMemoryVersions(context.Context, string, string) ([]Memory, error)
	SearchMemories(context.Context, string, []string, string, int) ([]Memory, error)
	PutRelationship(context.Context, Relationship, Event) (Relationship, error)
	GetRelationship(context.Context, string, string) (Relationship, error)
	ListRelationships(context.Context, string, string) ([]Relationship, error)
	GetEventByIdempotency(context.Context, string, string) (*Event, error)
	ListEvents(context.Context, string) ([]Event, error)
	Export(context.Context, string) (Snapshot, error)
	Import(context.Context, Snapshot) error
	RebuildProjections(context.Context, string) error
	PutEmbedding(context.Context, string, string, string, int, []float32, string, string) error
	SetEmbeddingState(context.Context, string, string, int, string) error
	SearchEmbeddings(context.Context, string, []string, []float32, int) (map[string]float64, error)
	EnqueueEmbedding(context.Context, string, string, int, string) error
	ClaimEmbedding(context.Context) (*EmbeddingJob, error)
	FinishEmbedding(context.Context, EmbeddingJob, error) error
	EmbeddingBacklog(context.Context, string) (int, error)
	QueueEmbeddings(context.Context, string, string) (int, error)
}
