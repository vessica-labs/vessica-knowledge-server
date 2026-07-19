package knowledge

import (
	"context"
	"sort"
	"strings"
	"time"
)

const (
	retrievalCandidateLimit = 50
	rerankCandidateLimit    = 12
	retrievalEmbedTimeout   = 400 * time.Millisecond
)

// RetrieveMemories is the shared retrieval-v2 path used by the narrow memory
// API and context assembly. Store-level queries remain available for backwards
// compatible lexical administration.
func (s *Service) RetrieveMemories(ctx context.Context, request MemoryRetrievalRequest) (MemoryRetrievalResponse, error) {
	if request.WorkspaceID == "" {
		return MemoryRetrievalResponse{}, Invalid("workspace_id required")
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Limit <= 0 {
		request.Limit = 20
	}
	if request.Limit > 100 {
		return MemoryRetrievalResponse{}, Invalid("limit must not exceed 100")
	}
	if request.Rerank == "" {
		request.Rerank = "auto"
	}
	if request.Rerank != "auto" && request.Rerank != "never" && request.Rerank != "always" {
		return MemoryRetrievalResponse{}, Invalid("rerank must be auto, never, or always")
	}
	if len(request.ScopeIDs) > 0 {
		expanded, err := s.Store.ScopeAncestors(ctx, request.WorkspaceID, request.ScopeIDs)
		if err != nil {
			return MemoryRetrievalResponse{}, err
		}
		request.ScopeIDs = expanded
	}

	lexical, err := s.Store.SearchMemoryCandidates(ctx, request.WorkspaceID, request.ScopeIDs, request.Query, retrievalCandidateLimit)
	if err != nil {
		return MemoryRetrievalResponse{}, err
	}
	entities, err := s.Store.ResolveEntities(ctx, request.WorkspaceID, request.ScopeIDs, request.Query)
	if err != nil {
		return MemoryRetrievalResponse{}, err
	}
	entitySet := map[string]bool{}
	for _, entity := range entities {
		entitySet[entity.ID] = true
	}
	for _, id := range request.EntityIDs {
		entitySet[id] = true
		if entity, getErr := s.Store.GetEntity(ctx, request.WorkspaceID, id); getErr == nil && !containsEntity(entities, id) {
			entities = append(entities, entity)
		}
	}
	enforceEntity := len(request.EntityIDs) > 0 || len(entities) == 1

	semanticScores := map[string]float64{}
	mode := "lexical"
	model := ""
	if s.Embedder != nil && request.Query != "" {
		embedCtx, cancel := context.WithTimeout(ctx, retrievalEmbedTimeout)
		vector, embedErr := s.Embedder.Embed(embedCtx, request.Query)
		cancel()
		if embedErr == nil {
			if scores, searchErr := s.Store.SearchEmbeddings(ctx, request.WorkspaceID, request.ScopeIDs, vector, retrievalCandidateLimit); searchErr == nil {
				semanticScores = scores
				mode = "semantic_hybrid"
				model = s.Embedder.Model()
			}
		}
	}

	candidates := map[string]Memory{}
	lexicalRank := map[string]int{}
	for index, memory := range lexical {
		if retrievalEligible(memory, request.ScopeIDs, time.Now()) {
			candidates[memory.ID] = memory
			lexicalRank[memory.ID] = index + 1
		}
	}
	semanticIDs := make([]string, 0, len(semanticScores))
	for id := range semanticScores {
		semanticIDs = append(semanticIDs, id)
	}
	sort.Slice(semanticIDs, func(i, j int) bool {
		if semanticScores[semanticIDs[i]] == semanticScores[semanticIDs[j]] {
			return semanticIDs[i] < semanticIDs[j]
		}
		return semanticScores[semanticIDs[i]] > semanticScores[semanticIDs[j]]
	})
	semanticRank := map[string]int{}
	missingSemanticIDs := make([]string, 0, len(semanticIDs))
	for index, id := range semanticIDs {
		semanticRank[id] = index + 1
		if _, ok := candidates[id]; ok {
			continue
		}
		missingSemanticIDs = append(missingSemanticIDs, id)
	}
	if semanticMemories, getErr := s.Store.GetCurrentMemories(ctx, request.WorkspaceID, missingSemanticIDs); getErr == nil {
		for _, memory := range semanticMemories {
			if retrievalEligible(memory, request.ScopeIDs, time.Now()) {
				candidates[memory.ID] = memory
			}
		}
	}
	entityIDs := make([]string, 0, len(entitySet))
	for id := range entitySet {
		entityIDs = append(entityIDs, id)
	}
	entityRelated, _ := s.Store.RelatedObjectIDs(ctx, request.WorkspaceID, entityIDs)

	canonicalSubject := canonicalCandidateSubject(request.Query, candidates)

	ranked := make([]RankedMemory, 0, len(candidates))
	for _, memory := range candidates {
		if canonicalSubject != "" && !strings.EqualFold(strings.TrimSpace(memory.Subject), canonicalSubject) {
			continue
		}
		entityMatch := entityRelated[memory.ID] || memoryMentionsEntity(memory, entities)
		if enforceEntity && !entityMatch {
			continue
		}
		lexicalComponent := reciprocalRank(lexicalRank[memory.ID])
		semanticComponent := reciprocalRank(semanticRank[memory.ID])
		entityComponent := 0.0
		if entityMatch {
			entityComponent = 1
		}
		scopeComponent := scopeScore(request.ScopeIDs, memory.ScopeID)
		recencyComponent := 0.0
		if memory.Type == "episode" {
			recencyComponent = recencyScore(memory.UpdatedAt)
		}
		lexicalWeight, semanticWeight := .65, 0.0
		if mode == "semantic_hybrid" {
			lexicalWeight, semanticWeight = .36, .29
		}
		score := lexicalWeight*lexicalComponent + semanticWeight*semanticComponent + .15*entityComponent + .1*scopeComponent + .05*memory.Importance + .05*memory.Confidence + .05*recencyComponent
		ranked = append(ranked, RankedMemory{Memory: memory, Score: score, Explanation: map[string]float64{
			"lexical": lexicalComponent, "semantic": semanticComponent, "entity": entityComponent,
			"scope": scopeComponent, "importance": memory.Importance, "confidence": memory.Confidence,
			"recency": recencyComponent,
		}})
	}
	sortRankedMemories(ranked)

	response := MemoryRetrievalResponse{
		Schema: APIVersion, Results: ranked, Ranking: rankingMechanism(), RetrievalMode: mode,
		EmbeddingModel: model, IndexFresh: true,
		Rerank: RerankMetadata{Reason: "disabled", CandidateCount: min(len(ranked), rerankCandidateLimit)},
	}
	if mode == "lexical" {
		response.Ranking.MemoryWeights["lexical_rrf"] = .65
		response.Ranking.MemoryWeights["semantic_rrf"] = 0
	}
	if backlog, backlogErr := s.Store.EmbeddingBacklog(ctx, request.WorkspaceID); backlogErr == nil {
		response.IndexFresh = backlog == 0
	}
	if len(request.EntityIDs) == 0 && (len(entities) > 1 || ambiguousCandidateSubjects(request.Query, ranked)) {
		response.Ambiguity = "ambiguous_subject"
	}
	if response.Ambiguity == "" {
		response = s.maybeRerank(ctx, request, response)
	} else {
		response.Rerank.Reason = "ambiguous_subject"
	}
	if len(response.Results) > request.Limit {
		response.Results = response.Results[:request.Limit]
	}
	return response, nil
}

func retrievalEligible(memory Memory, scopes []string, now time.Time) bool {
	if memory.State != "active" || !includes(scopes, memory.ScopeID) {
		return false
	}
	return (memory.ValidFrom == nil || !memory.ValidFrom.After(now)) && (memory.ValidUntil == nil || !memory.ValidUntil.Before(now))
}

func reciprocalRank(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 61 / float64(60+rank)
}

func sortRankedMemories(ranked []RankedMemory) {
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		if ranked[i].Memory.Version != ranked[j].Memory.Version {
			return ranked[i].Memory.Version > ranked[j].Memory.Version
		}
		if !ranked[i].Memory.UpdatedAt.Equal(ranked[j].Memory.UpdatedAt) {
			return ranked[i].Memory.UpdatedAt.After(ranked[j].Memory.UpdatedAt)
		}
		return ranked[i].Memory.ID < ranked[j].Memory.ID
	})
}

func (s *Service) maybeRerank(ctx context.Context, request MemoryRetrievalRequest, response MemoryRetrievalResponse) MemoryRetrievalResponse {
	if request.Rerank == "never" || s.Reranker == nil {
		if request.Rerank == "never" {
			response.Rerank.Reason = "request_disabled"
		}
		return response
	}
	count := min(len(response.Results), rerankCandidateLimit)
	if count < 3 {
		response.Rerank.Reason = "insufficient_candidates"
		return response
	}
	margin := response.Results[0].Score - response.Results[1].Score
	if request.Rerank == "auto" && margin >= .08 && canonicalSubjectMatch(request.Query, response.Results[0].Memory) {
		response.Rerank.Reason = "confident_hybrid_match"
		return response
	}
	candidates := make([]Memory, count)
	for index := range candidates {
		candidates[index] = response.Results[index].Memory
	}
	started := time.Now()
	rerankCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	result, err := s.Reranker.Rerank(rerankCtx, request.Query, candidates)
	response.Rerank = RerankMetadata{Model: s.Reranker.Model(), Reason: "provider_error", CandidateCount: count, LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		return response
	}
	byID := map[string]RankedMemory{}
	for _, item := range response.Results[:count] {
		byID[item.Memory.ID] = item
	}
	seen := map[string]bool{}
	ordered := make([]RankedMemory, 0, len(response.Results))
	for index, id := range result.IDs {
		item, ok := byID[id]
		if !ok || seen[id] {
			response.Rerank.Reason = "invalid_provider_result"
			return response
		}
		seen[id] = true
		item.Explanation["reranker"] = reciprocalRank(index + 1)
		item.Score = .7*item.Explanation["reranker"] + .3*item.Score
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		response.Rerank.Reason = "invalid_provider_result"
		return response
	}
	for _, item := range response.Results {
		if !seen[item.Memory.ID] {
			ordered = append(ordered, item)
		}
	}
	response.Results = ordered
	response.Rerank = RerankMetadata{Applied: true, Model: s.Reranker.Model(), Reason: "ambiguous_hybrid_candidates", CandidateCount: count, LatencyMS: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens}
	return response
}

func canonicalSubjectMatch(query string, memory Memory) bool {
	query = strings.ToLower(query)
	for _, candidate := range []string{memory.Subject, memory.Title} {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate != "" && strings.Contains(query, candidate) {
			return true
		}
	}
	return false
}

func memoryMentionsEntity(memory Memory, entities []Entity) bool {
	text := strings.ToLower(strings.Join([]string{memory.Subject, memory.Title, memory.Content}, " "))
	for _, entity := range entities {
		for _, name := range append([]string{entity.DisplayName}, entity.Aliases...) {
			name = strings.TrimSpace(strings.ToLower(name))
			if name != "" && strings.Contains(text, name) {
				return true
			}
		}
	}
	return false
}

func ambiguousCandidateSubjects(query string, ranked []RankedMemory) bool {
	query = strings.ToLower(query)
	subjects := map[string]bool{}
	for _, candidate := range ranked {
		if candidate.Memory.Type == "episode" {
			continue
		}
		subject := strings.TrimSpace(strings.ToLower(candidate.Memory.Subject))
		if subject == "" {
			continue
		}
		if strings.Contains(query, subject) {
			return false
		}
		subjects[subject] = true
	}
	return len(subjects) > 1
}

func canonicalCandidateSubject(query string, candidates map[string]Memory) string {
	query = strings.ToLower(query)
	matches := map[string]string{}
	for _, candidate := range candidates {
		subject := strings.TrimSpace(candidate.Subject)
		if subject != "" && strings.Contains(query, strings.ToLower(subject)) {
			matches[strings.ToLower(subject)] = subject
		}
	}
	if len(matches) != 1 {
		return ""
	}
	for _, subject := range matches {
		return subject
	}
	return ""
}
