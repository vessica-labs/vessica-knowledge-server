package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	Provider() string
	Model() string
}
type Service struct {
	Store    Store
	Embedder Embedder
	once     sync.Once
}

func NewService(store Store, embedder Embedder) *Service {
	return &Service{Store: store, Embedder: embedder}
}
func (s *Service) StartEmbeddingWorker(ctx context.Context) {
	if s.Embedder == nil {
		return
	}
	s.once.Do(func() {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					j, claimErr := s.Store.ClaimEmbedding(ctx)
					if claimErr != nil || j == nil {
						continue
					}
					v, err := s.Embedder.Embed(ctx, j.Text)
					if err != nil {
						_ = s.Store.FinishEmbedding(context.Background(), *j, err)
						if j.Attempts >= 5 {
							_ = s.Store.SetEmbeddingState(context.Background(), j.WorkspaceID, j.MemoryID, j.Version, "failed")
						}
						continue
					}
					_ = s.Store.PutEmbedding(context.Background(), j.WorkspaceID, "memory", j.MemoryID, j.Version, v, s.Embedder.Provider(), s.Embedder.Model())
					_ = s.Store.SetEmbeddingState(context.Background(), j.WorkspaceID, j.MemoryID, j.Version, "ready")
					_ = s.Store.FinishEmbedding(context.Background(), *j, nil)
				}
			}
		}()
	})
}

func validateWrite(o WriteOptions) error {
	if o.WorkspaceID == "" {
		return Invalid("workspace_id required")
	}
	if o.IdempotencyKey == "" {
		return Invalid("idempotency_key required")
	}
	if o.Actor.ID == "" || o.Actor.Type == "" {
		return Invalid("actor required")
	}
	if o.Provenance.Source == "" {
		return Invalid("provenance source required")
	}
	return nil
}
func event(o WriteOptions, aggType, aggID, typ string, version int, payload map[string]any) Event {
	return Event{ID: NewID("kevt"), WorkspaceID: o.WorkspaceID, AggregateType: aggType, AggregateID: aggID, AggregateVersion: version, Type: typ, Actor: o.Actor, Provenance: o.Provenance, IdempotencyKey: o.IdempotencyKey, Payload: payload, OccurredAt: time.Now().UTC()}
}
func (s *Service) replay(ctx context.Context, o WriteOptions) (*Event, error) {
	if err := validateWrite(o); err != nil {
		return nil, err
	}
	return s.Store.GetEventByIdempotency(ctx, o.WorkspaceID, o.IdempotencyKey)
}

func (s *Service) CreateScope(ctx context.Context, o WriteOptions, v Scope) (Scope, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetScope(ctx, o.WorkspaceID, old.AggregateID)
	}
	if v.Type == "" || v.Name == "" || v.CanonicalKey == "" {
		return v, Invalid("scope type, name, and canonical_key required")
	}
	if v.ParentID != "" {
		if _, err := s.Store.GetScope(ctx, o.WorkspaceID, v.ParentID); err != nil {
			return v, Invalid("parent scope does not exist in workspace")
		}
	}
	if v.ID == "" {
		v.ID = NewID("scope")
	}
	v.WorkspaceID = o.WorkspaceID
	v.CreatedAt = time.Now().UTC()
	return s.Store.CreateScope(ctx, v, event(o, "scope", v.ID, "scope.created", 1, map[string]any{"scope": v}))
}
func (s *Service) CreateEntity(ctx context.Context, o WriteOptions, v Entity) (Entity, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetEntity(ctx, o.WorkspaceID, old.AggregateID)
	}
	if v.ScopeID == "" || v.Type == "" || v.DisplayName == "" {
		return v, Invalid("scope_id, type, and display_name required")
	}
	if v.ID == "" {
		v.ID = NewID("ent")
	}
	v.WorkspaceID = o.WorkspaceID
	v.Version = 1
	v.State = defaultString(v.State, "active")
	if !validObjectState(v.State) {
		return v, Invalid("invalid entity state")
	}
	v.Provenance = o.Provenance
	v.CreatedAt = time.Now().UTC()
	v.UpdatedAt = v.CreatedAt
	normalizeMaps(&v.Metadata)
	return s.Store.PutEntity(ctx, v, event(o, "entity", v.ID, "entity.created", v.Version, map[string]any{"entity": v}))
}
func (s *Service) VersionEntity(ctx context.Context, o WriteOptions, id string, v Entity) (Entity, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetEntity(ctx, o.WorkspaceID, id)
	}
	cur, err := s.Store.GetEntity(ctx, o.WorkspaceID, id)
	if err != nil {
		return v, err
	}
	v.ID = id
	v.WorkspaceID = o.WorkspaceID
	v.Version = cur.Version + 1
	if v.ScopeID == "" {
		v.ScopeID = cur.ScopeID
	}
	if v.Type == "" {
		v.Type = cur.Type
	}
	if v.DisplayName == "" {
		v.DisplayName = cur.DisplayName
	}
	v.State = defaultString(v.State, cur.State)
	if !validObjectState(v.State) {
		return v, Invalid("invalid entity state")
	}
	v.Provenance = o.Provenance
	v.CreatedAt = cur.CreatedAt
	v.UpdatedAt = time.Now().UTC()
	return s.Store.PutEntity(ctx, v, event(o, "entity", id, "entity.versioned", v.Version, map[string]any{"entity": v}))
}

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Service) CreateArtifact(ctx context.Context, o WriteOptions, v Artifact) (Artifact, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetArtifact(ctx, o.WorkspaceID, old.AggregateID, 0)
	}
	if v.ScopeID == "" || v.Type == "" || v.Title == "" {
		return v, Invalid("scope_id, type, and title required")
	}
	if len(v.Content) > 4<<20 {
		return v, Invalid("artifact content exceeds 4 MiB")
	}
	if !validArtifactStatus(defaultString(v.Status, "draft")) {
		return v, Invalid("invalid artifact status")
	}
	if v.ID == "" {
		v.ID = NewID("art")
	}
	v.WorkspaceID = o.WorkspaceID
	v.Version = 1
	v.Status = defaultString(v.Status, "draft")
	v.ContentHash = hash(v.Content)
	v.Provenance = o.Provenance
	v.CreatedAt = time.Now().UTC()
	v.UpdatedAt = v.CreatedAt
	normalizeMaps(&v.Metadata)
	return s.Store.PutArtifact(ctx, v, event(o, "artifact", v.ID, "artifact.created", v.Version, map[string]any{"artifact": v}))
}
func (s *Service) VersionArtifact(ctx context.Context, o WriteOptions, id string, v Artifact) (Artifact, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetArtifact(ctx, o.WorkspaceID, id, 0)
	}
	cur, err := s.Store.GetArtifact(ctx, o.WorkspaceID, id, 0)
	if err != nil {
		return v, err
	}
	v.ID = id
	v.WorkspaceID = o.WorkspaceID
	v.Version = cur.Version + 1
	if v.ScopeID == "" {
		v.ScopeID = cur.ScopeID
	}
	if v.Type == "" {
		v.Type = cur.Type
	}
	if v.Title == "" {
		v.Title = cur.Title
	}
	v.Status = defaultString(v.Status, cur.Status)
	if len(v.Content) > 4<<20 {
		return v, Invalid("artifact content exceeds 4 MiB")
	}
	if !validArtifactStatus(v.Status) {
		return v, Invalid("invalid artifact status")
	}
	v.ContentHash = hash(v.Content)
	v.Provenance = o.Provenance
	v.CreatedAt = cur.CreatedAt
	v.UpdatedAt = time.Now().UTC()
	return s.Store.PutArtifact(ctx, v, event(o, "artifact", id, "artifact.versioned", v.Version, map[string]any{"artifact": v}))
}
func (s *Service) SetArtifactStatus(ctx context.Context, o WriteOptions, id, status string) (Artifact, error) {
	if !validArtifactStatus(status) {
		return Artifact{}, Invalid("invalid artifact status")
	}
	cur, err := s.Store.GetArtifact(ctx, o.WorkspaceID, id, 0)
	if err != nil {
		return cur, err
	}
	cur.Status = status
	return s.VersionArtifact(ctx, o, id, cur)
}

var memoryTypes = map[string]bool{"instruction": true, "fact": true, "decision": true, "episode": true}
var confidenceSources = map[string]bool{"human_confirmed": true, "agent_inferred": true, "imported": true, "external_system": true, "observed": true}

func (s *Service) CreateMemory(ctx context.Context, o WriteOptions, v Memory) (Memory, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetMemory(ctx, o.WorkspaceID, old.AggregateID, 0)
	}
	if !memoryTypes[v.Type] || v.ScopeID == "" || v.Content == "" {
		return v, Invalid("valid type, scope_id, and content required")
	}
	if len(v.Content) > 1<<20 {
		return v, Invalid("memory content exceeds 1 MiB")
	}
	if v.ID == "" {
		v.ID = NewID("mem")
	}
	v.WorkspaceID = o.WorkspaceID
	v.Version = 1
	v.State = defaultString(v.State, "active")
	if !validObjectState(v.State) {
		return v, Invalid("invalid memory state")
	}
	v.ConfidenceSource = defaultString(v.ConfidenceSource, "agent_inferred")
	if !confidenceSources[v.ConfidenceSource] || v.Importance < 0 || v.Importance > 1 || v.Confidence < 0 || v.Confidence > 1 {
		return v, Invalid("invalid confidence source, importance, or confidence")
	}
	if v.Importance == 0 {
		v.Importance = .5
	}
	if v.Confidence == 0 {
		v.Confidence = .5
	}
	if s.Embedder == nil {
		v.EmbeddingState = "not_configured"
	} else {
		v.EmbeddingState = "pending"
	}
	v.Provenance = o.Provenance
	v.CreatedAt = time.Now().UTC()
	v.UpdatedAt = v.CreatedAt
	normalizeMaps(&v.Metadata)
	got, err := s.Store.PutMemory(ctx, v, event(o, "memory", v.ID, "memory.created", v.Version, map[string]any{"memory": v}))
	return got, err
}
func (s *Service) VersionMemory(ctx context.Context, o WriteOptions, id string, v Memory) (Memory, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetMemory(ctx, o.WorkspaceID, id, 0)
	}
	cur, err := s.Store.GetMemory(ctx, o.WorkspaceID, id, 0)
	if err != nil {
		return v, err
	}
	v.ID = id
	v.WorkspaceID = o.WorkspaceID
	v.Version = cur.Version + 1
	if v.ScopeID == "" {
		v.ScopeID = cur.ScopeID
	}
	if v.Type == "" {
		v.Type = cur.Type
	}
	if v.Content == "" {
		v.Content = cur.Content
	}
	v.State = defaultString(v.State, cur.State)
	if !validObjectState(v.State) {
		return v, Invalid("invalid memory state")
	}
	v.ConfidenceSource = defaultString(v.ConfidenceSource, cur.ConfidenceSource)
	if len(v.Content) > 1<<20 {
		return v, Invalid("memory content exceeds 1 MiB")
	}
	if !confidenceSources[v.ConfidenceSource] || v.Importance < 0 || v.Importance > 1 || v.Confidence < 0 || v.Confidence > 1 {
		return v, Invalid("invalid confidence source, importance, or confidence")
	}
	if v.Importance == 0 {
		v.Importance = cur.Importance
	}
	if v.Confidence == 0 {
		v.Confidence = cur.Confidence
	}
	if s.Embedder == nil {
		v.EmbeddingState = "not_configured"
	} else {
		v.EmbeddingState = "pending"
	}
	v.Provenance = o.Provenance
	v.CreatedAt = cur.CreatedAt
	v.UpdatedAt = time.Now().UTC()
	got, err := s.Store.PutMemory(ctx, v, event(o, "memory", id, "memory.versioned", v.Version, map[string]any{"memory": v}))
	return got, err
}
func (s *Service) SetMemoryState(ctx context.Context, o WriteOptions, id, state string) (Memory, error) {
	if !validObjectState(state) {
		return Memory{}, Invalid("invalid memory state")
	}
	cur, err := s.Store.GetMemory(ctx, o.WorkspaceID, id, 0)
	if err != nil {
		return cur, err
	}
	cur.State = state
	return s.VersionMemory(ctx, o, id, cur)
}

func (s *Service) CreateRelationship(ctx context.Context, o WriteOptions, v Relationship) (Relationship, error) {
	old, err := s.replay(ctx, o)
	if err != nil {
		return v, err
	}
	if old != nil {
		return s.Store.GetRelationship(ctx, o.WorkspaceID, old.AggregateID)
	}
	if v.ScopeID == "" || v.FromType == "" || v.FromID == "" || v.Predicate == "" || v.ToType == "" || v.ToID == "" {
		return v, Invalid("relationship endpoints, predicate, and scope required")
	}
	if v.ID == "" {
		v.ID = NewID("rel")
	}
	v.WorkspaceID = o.WorkspaceID
	v.Version = 1
	v.State = defaultString(v.State, "active")
	if !validObjectState(v.State) {
		return v, Invalid("invalid relationship state")
	}
	if v.Confidence == 0 {
		v.Confidence = 1
	}
	v.Provenance = o.Provenance
	v.CreatedAt = time.Now().UTC()
	normalizeMaps(&v.Metadata)
	return s.Store.PutRelationship(ctx, v, event(o, "relationship", v.ID, "relationship.created", 1, map[string]any{"relationship": v}))
}

func (s *Service) Context(ctx context.Context, r ContextRequest) (ContextResponse, error) {
	if r.WorkspaceID == "" {
		return ContextResponse{}, Invalid("workspace_id required")
	}
	if r.TokenBudget <= 0 {
		r.TokenBudget = 12000
	}
	if len(r.ScopeIDs) > 0 {
		expanded, err := s.Store.ScopeAncestors(ctx, r.WorkspaceID, r.ScopeIDs)
		if err != nil {
			return ContextResponse{}, err
		}
		r.ScopeIDs = expanded
	}
	arts, err := s.Store.ListArtifacts(ctx, r.WorkspaceID, r.ScopeIDs, r.ArtifactSelectors)
	if err != nil {
		return ContextResponse{}, err
	}
	mems, err := s.Store.SearchMemories(ctx, r.WorkspaceID, r.ScopeIDs, r.Query, 200)
	if err != nil {
		return ContextResponse{}, err
	}
	entities, err := s.Store.ResolveEntities(ctx, r.WorkspaceID, r.ScopeIDs, r.Query)
	if err != nil {
		return ContextResponse{}, err
	}
	entitySet := map[string]bool{}
	for _, entity := range entities {
		entitySet[entity.ID] = true
	}
	for _, id := range r.EntityIDs {
		entitySet[id] = true
		if !containsEntity(entities, id) {
			if entity, getErr := s.Store.GetEntity(ctx, r.WorkspaceID, id); getErr == nil {
				entities = append(entities, entity)
			}
		}
	}
	if len(entitySet) > 0 {
		sort.SliceStable(arts, func(i, j int) bool {
			return s.relatedToAny(ctx, r.WorkspaceID, arts[i].ID, entitySet) && !s.relatedToAny(ctx, r.WorkspaceID, arts[j].ID, entitySet)
		})
	}
	semantic := map[string]float64{}
	mode := "lexical"
	model := ""
	if s.Embedder != nil {
		if qv, e := s.Embedder.Embed(ctx, r.Query); e == nil {
			semantic, _ = s.Store.SearchEmbeddings(ctx, r.WorkspaceID, r.ScopeIDs, qv, 200)
			seen := map[string]bool{}
			for _, m := range mems {
				seen[m.ID] = true
			}
			for id := range semantic {
				if seen[id] {
					continue
				}
				if m, getErr := s.Store.GetMemory(ctx, r.WorkspaceID, id, 0); getErr == nil && includes(r.ScopeIDs, m.ScopeID) && m.State != "archived" && m.State != "tombstoned" {
					mems = append(mems, m)
				}
			}
			mode = "semantic_hybrid"
			model = s.Embedder.Model()
		}
	}
	now := time.Now()
	ranked := make([]RankedMemory, 0, len(mems))
	for _, m := range mems {
		if m.ValidFrom != nil && m.ValidFrom.After(now) || m.ValidUntil != nil && m.ValidUntil.Before(now) {
			continue
		}
		lex := lexicalScore(r.Query, m.Title+" "+m.Content)
		scope := scopeScore(r.ScopeIDs, m.ScopeID)
		recency := recencyScore(m.UpdatedAt)
		sem := semantic[m.ID]
		entity := 0.0
		if s.relatedToAny(ctx, r.WorkspaceID, m.ID, entitySet) {
			entity = 1
		}
		score := .3*lex + .18*scope + .14*m.Importance + .14*m.Confidence + .09*recency + .05*sem + .1*entity
		ranked = append(ranked, RankedMemory{m, score, map[string]float64{"lexical": lex, "scope": scope, "entity": entity, "importance": m.Importance, "confidence": m.Confidence, "recency": recency, "semantic": sem}})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	backlog, _ := s.Store.EmbeddingBacklog(ctx, r.WorkspaceID)
	resp := ContextResponse{Schema: APIVersion, Ranking: rankingMechanism(), RetrievalMode: mode, Artifacts: []Artifact{}, ArtifactExplanations: []ArtifactExplanation{}, Instructions: []RankedMemory{}, Entities: entities, Decisions: []RankedMemory{}, Facts: []RankedMemory{}, Episodes: []RankedMemory{}, IndexFresh: backlog == 0, EmbeddingModel: model}
	used := 0
	instructionKeys := map[string]bool{}
	for _, a := range arts {
		cost := tokens(a.Title + a.Content)
		if used+cost > r.TokenBudget {
			resp.Omissions = append(resp.Omissions, "artifact:"+a.ID)
			continue
		}
		resp.Artifacts = append(resp.Artifacts, a)
		resp.ArtifactExplanations = append(resp.ArtifactExplanations, ArtifactExplanation{ArtifactID: a.ID, Reasons: artifactReasons(a, r, entitySet, s.relatedToAny(ctx, r.WorkspaceID, a.ID, entitySet))})
		used += cost
	}
	for _, m := range ranked {
		cost := tokens(m.Memory.Title + m.Memory.Content)
		if used+cost > r.TokenBudget {
			resp.Omissions = append(resp.Omissions, "memory:"+m.Memory.ID)
			continue
		}
		switch m.Memory.Type {
		case "instruction":
			if key, _ := m.Memory.Metadata["semantic_key"].(string); key != "" {
				if instructionKeys[key] {
					resp.Omissions = append(resp.Omissions, "instruction_override:"+m.Memory.ID)
					continue
				}
				instructionKeys[key] = true
			}
			resp.Instructions = append(resp.Instructions, m)
		case "decision":
			resp.Decisions = append(resp.Decisions, m)
		case "fact":
			resp.Facts = append(resp.Facts, m)
		case "episode":
			resp.Episodes = append(resp.Episodes, m)
		}
		used += cost
	}
	resp.TokenEstimate = used
	return resp, nil
}

func (s *Service) IngestWorkflowEvent(ctx context.Context, o WriteOptions, w WorkflowEvent) (Memory, error) {
	if w.ID == "" || w.Type == "" || w.Summary == "" {
		return Memory{}, Invalid("workflow event id, type, and summary required")
	}
	if !meaningfulWorkflowEvent(w.Type) {
		return Memory{}, Invalid("workflow event is not knowledge-worthy")
	}
	m := Memory{Type: "episode", ScopeID: w.RepositoryScopeID, Title: workflowTitle(w.Type), Content: w.Summary, Importance: .65, Confidence: .9, ConfidenceSource: "observed", Metadata: map[string]any{"workflow_event_id": w.ID, "workflow_event_type": w.Type, "references": w.References}}
	o.IdempotencyKey = "workflow:" + w.ID
	o.Provenance = Provenance{Source: "vessica_workflow", SourceID: w.ID, Detail: w.Metadata}
	got, err := s.CreateMemory(ctx, o, m)
	if err != nil {
		return got, err
	}
	for i, ref := range w.References {
		_, _ = s.CreateRelationship(ctx, WriteOptions{WorkspaceID: o.WorkspaceID, IdempotencyKey: fmt.Sprintf("workflow:%s:ref:%d", w.ID, i), Actor: o.Actor, Provenance: o.Provenance}, Relationship{ScopeID: w.RepositoryScopeID, FromType: "memory", FromID: got.ID, Predicate: "references", ToType: "external", ToID: ref.System + ":" + ref.ID, Confidence: 1, Metadata: map[string]any{"url": ref.URL}})
	}
	return got, nil
}

// ImportSnapshot restores the append-only stream and projections, then queues
// imported memory versions for the hosted embedding provider without blocking
// lexical availability.
func (s *Service) ImportSnapshot(ctx context.Context, snap Snapshot) error {
	if err := s.Store.Import(ctx, snap); err != nil {
		return err
	}
	if s.Embedder == nil {
		return nil
	}
	for _, m := range snap.Memories {
		if err := s.Store.SetEmbeddingState(ctx, m.WorkspaceID, m.ID, m.Version, "pending"); err != nil {
			return err
		}
		if err := s.Store.EnqueueEmbedding(ctx, m.WorkspaceID, m.ID, m.Version, m.Title+"\n"+m.Content); err != nil {
			return err
		}
	}
	return nil
}

func meaningfulWorkflowEvent(t string) bool {
	switch t {
	case "epic.accepted", "epic.planned", "epic.completed", "epic.failed", "epic.cancelled", "run.completed", "run.failed", "run.approved", "run.merged", "run.rolled_back", "run.cancelled", "run.refined", "ticket.completed", "ticket.blocked", "ticket.discovered", "sandbox.refined":
		return true
	}
	return false
}
func workflowTitle(t string) string { return strings.Title(strings.ReplaceAll(t, ".", " ")) }
func defaultString(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
func normalizeMaps(v *map[string]any) {
	if *v == nil {
		*v = map[string]any{}
	}
}
func tokens(s string) int {
	n := len([]rune(s)) / 4
	if n < 1 {
		return 1
	}
	return n
}
func lexicalScore(q, text string) float64 {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return .1
	}
	t := strings.ToLower(text)
	hits := 0
	for _, w := range words {
		if strings.Contains(t, w) {
			hits++
		}
	}
	return float64(hits) / float64(len(words))
}
func scopeScore(scopes []string, s string) float64 {
	if len(scopes) == 0 {
		return .5
	}
	for i, x := range scopes {
		if x == s {
			return float64(i+1) / float64(len(scopes))
		}
	}
	return 0
}
func recencyScore(t time.Time) float64 {
	days := time.Since(t).Hours() / 24
	if days < 0 {
		return 1
	}
	return 1 / (1 + days/30)
}
func containsEntity(v []Entity, id string) bool {
	for _, x := range v {
		if x.ID == id {
			return true
		}
	}
	return false
}
func (s *Service) relatedToAny(ctx context.Context, workspace, objectID string, ids map[string]bool) bool {
	if len(ids) == 0 {
		return false
	}
	rels, err := s.Store.ListRelationships(ctx, workspace, objectID)
	if err != nil {
		return false
	}
	for _, rel := range rels {
		if ids[rel.FromID] || ids[rel.ToID] {
			return true
		}
	}
	return false
}

func validArtifactStatus(v string) bool {
	switch v {
	case "draft", "active", "superseded", "archived":
		return true
	}
	return false
}
func rankingMechanism() RankingMechanism {
	return RankingMechanism{
		Version:             "v1",
		MemoryWeights:       map[string]float64{"lexical": .30, "scope": .18, "importance": .14, "confidence": .14, "entity": .10, "recency": .09, "semantic": .05},
		ArtifactPolicy:      []string{"explicit identity and version", "active lifecycle and type selectors", "applicable scope", "entity relationship", "stable storage order"},
		ContextOrder:        []string{"active_artifacts", "instructions", "entities", "decisions", "facts", "episodes"},
		InstructionOverride: "A more-specific applicable instruction replaces a broader instruction only when both declare the same metadata.semantic_key.",
	}
}
func artifactReasons(a Artifact, r ContextRequest, entities map[string]bool, related bool) []string {
	reasons := []string{"deterministic_artifact_selection"}
	for _, selector := range r.ArtifactSelectors {
		if selector.ID == a.ID {
			reasons = append(reasons, "explicit_id")
		}
		if selector.Version == a.Version && selector.Version > 0 {
			reasons = append(reasons, "explicit_version")
		}
		if selector.Type == a.Type && selector.Type != "" {
			reasons = append(reasons, "type:"+a.Type)
		}
		if selector.Status == a.Status && selector.Status != "" {
			reasons = append(reasons, "status:"+a.Status)
		}
	}
	if includes(r.ScopeIDs, a.ScopeID) {
		reasons = append(reasons, "applicable_scope")
	}
	if len(entities) > 0 && related {
		reasons = append(reasons, "entity_relationship")
	}
	return reasons
}
func validObjectState(v string) bool {
	switch v {
	case "active", "superseded", "archived", "tombstoned":
		return true
	}
	return false
}
