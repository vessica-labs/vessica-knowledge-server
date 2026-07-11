package knowledge

import "time"

const APIVersion = "vessica.knowledge/v1"

type Actor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}
type Provenance struct {
	Source    string         `json:"source"`
	SourceID  string         `json:"source_id,omitempty"`
	SourceURL string         `json:"source_url,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

type Scope struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	ParentID     string    `json:"parent_id,omitempty"`
	Type         string    `json:"type"`
	Name         string    `json:"name"`
	CanonicalKey string    `json:"canonical_key"`
	CreatedAt    time.Time `json:"created_at"`
}
type ExternalRef struct {
	System string `json:"system"`
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
}

type Entity struct {
	ID           string         `json:"id"`
	WorkspaceID  string         `json:"workspace_id"`
	ScopeID      string         `json:"scope_id"`
	Version      int            `json:"version"`
	Type         string         `json:"type"`
	DisplayName  string         `json:"display_name"`
	Aliases      []string       `json:"aliases"`
	ExternalRefs []ExternalRef  `json:"external_refs"`
	Metadata     map[string]any `json:"metadata"`
	State        string         `json:"state"`
	Provenance   Provenance     `json:"provenance"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}
type Artifact struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	ScopeID     string         `json:"scope_id"`
	Version     int            `json:"version"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Status      string         `json:"status"`
	Content     string         `json:"content"`
	ContentHash string         `json:"content_hash"`
	SourceRef   *ExternalRef   `json:"source_ref,omitempty"`
	Metadata    map[string]any `json:"metadata"`
	Provenance  Provenance     `json:"provenance"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}
type Memory struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspace_id"`
	ScopeID          string         `json:"scope_id"`
	Version          int            `json:"version"`
	Type             string         `json:"type"`
	Subject          string         `json:"subject,omitempty"`
	Predicate        string         `json:"predicate,omitempty"`
	Object           string         `json:"object,omitempty"`
	Title            string         `json:"title"`
	Content          string         `json:"content"`
	Importance       float64        `json:"importance"`
	Confidence       float64        `json:"confidence"`
	ConfidenceSource string         `json:"confidence_source"`
	ValidFrom        *time.Time     `json:"valid_from,omitempty"`
	ValidUntil       *time.Time     `json:"valid_until,omitempty"`
	State            string         `json:"state"`
	EmbeddingState   string         `json:"embedding_state"`
	Metadata         map[string]any `json:"metadata"`
	Provenance       Provenance     `json:"provenance"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}
type Relationship struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	ScopeID     string         `json:"scope_id"`
	Version     int            `json:"version"`
	FromType    string         `json:"from_type"`
	FromID      string         `json:"from_id"`
	Predicate   string         `json:"predicate"`
	ToType      string         `json:"to_type"`
	ToID        string         `json:"to_id"`
	Confidence  float64        `json:"confidence"`
	State       string         `json:"state"`
	Metadata    map[string]any `json:"metadata"`
	Provenance  Provenance     `json:"provenance"`
	CreatedAt   time.Time      `json:"created_at"`
}
type Event struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspace_id"`
	AggregateType    string         `json:"aggregate_type"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int            `json:"aggregate_version"`
	Type             string         `json:"event_type"`
	Actor            Actor          `json:"actor"`
	Provenance       Provenance     `json:"provenance"`
	IdempotencyKey   string         `json:"idempotency_key"`
	Payload          map[string]any `json:"payload"`
	OccurredAt       time.Time      `json:"occurred_at"`
}

type ArtifactSelector struct {
	Type    string `json:"type,omitempty"`
	Status  string `json:"status,omitempty"`
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
}
type ContextRequest struct {
	WorkspaceID       string             `json:"workspace_id"`
	Query             string             `json:"query"`
	ScopeIDs          []string           `json:"scopes"`
	EntityIDs         []string           `json:"entities,omitempty"`
	ArtifactSelectors []ArtifactSelector `json:"artifact_selectors,omitempty"`
	TokenBudget       int                `json:"token_budget"`
}
type RankedMemory struct {
	Memory      Memory             `json:"memory"`
	Score       float64            `json:"score"`
	Explanation map[string]float64 `json:"explanation"`
}
type ContextResponse struct {
	Schema         string         `json:"schema"`
	RetrievalMode  string         `json:"retrieval_mode"`
	Artifacts      []Artifact     `json:"artifacts"`
	Instructions   []RankedMemory `json:"instructions"`
	Entities       []Entity       `json:"entities"`
	Decisions      []RankedMemory `json:"decisions"`
	Facts          []RankedMemory `json:"facts"`
	Episodes       []RankedMemory `json:"episodes"`
	TokenEstimate  int            `json:"token_estimate"`
	Omissions      []string       `json:"omissions,omitempty"`
	IndexFresh     bool           `json:"index_fresh"`
	EmbeddingModel string         `json:"embedding_model,omitempty"`
}

type WorkflowEvent struct {
	ID                string         `json:"id"`
	WorkspaceID       string         `json:"workspace_id"`
	RepositoryScopeID string         `json:"repository_scope_id"`
	Type              string         `json:"type"`
	Summary           string         `json:"summary"`
	OccurredAt        time.Time      `json:"occurred_at"`
	Actor             Actor          `json:"actor"`
	References        []ExternalRef  `json:"references"`
	Metadata          map[string]any `json:"metadata"`
}
type Snapshot struct {
	Schema        string         `json:"schema"`
	WorkspaceID   string         `json:"workspace_id"`
	ExportedAt    time.Time      `json:"exported_at"`
	HighWatermark string         `json:"high_watermark"`
	Scopes        []Scope        `json:"scopes"`
	Entities      []Entity       `json:"entities"`
	Artifacts     []Artifact     `json:"artifacts"`
	Memories      []Memory       `json:"memories"`
	Relationships []Relationship `json:"relationships"`
	Events        []Event        `json:"events"`
	Counts        map[string]int `json:"counts"`
	Checksum      string         `json:"checksum"`
}

type WriteOptions struct {
	WorkspaceID    string     `json:"workspace_id"`
	IdempotencyKey string     `json:"idempotency_key"`
	Actor          Actor      `json:"actor"`
	Provenance     Provenance `json:"provenance"`
}
