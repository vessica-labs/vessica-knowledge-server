package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPReranker struct {
	APIKey    string
	BaseURL   string
	ModelName string
	Client    *http.Client
}

func (r *HTTPReranker) Model() string { return r.ModelName }

func (r *HTTPReranker) Rerank(ctx context.Context, query string, memories []Memory) (RerankResult, error) {
	if r.APIKey == "" || r.ModelName == "" {
		return RerankResult{}, fmt.Errorf("reranker API key and model required")
	}
	type candidate struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	candidates := make([]candidate, len(memories))
	for index, memory := range memories {
		candidates[index] = candidate{ID: memory.ID, Title: memory.Title, Content: memory.Content}
	}
	candidateJSON, _ := json.Marshal(candidates)
	prompt := "Rank only the supplied memory IDs by relevance to the query. Omit irrelevant IDs. Never invent an ID or fact.\nQuery: " + query + "\nCandidates: " + string(candidateJSON)
	body, _ := json.Marshal(map[string]any{
		"model": r.ModelName, "store": false,
		"reasoning": map[string]string{"effort": "none"},
		"input":     prompt,
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "memory_ranking", "strict": true,
			"schema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"ids"}, "properties": map[string]any{"ids": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}}},
		}},
	})
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/responses", bytes.NewReader(body))
	if err != nil {
		return RerankResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+r.APIKey)
	request.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 2500 * time.Millisecond}
	}
	response, err := client.Do(request)
	if err != nil {
		return RerankResult{}, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 300 {
		return RerankResult{}, fmt.Errorf("reranker provider failed (%d)", response.StatusCode)
	}
	var envelope struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return RerankResult{}, err
	}
	text := ""
	for _, output := range envelope.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" {
				text = content.Text
				break
			}
		}
	}
	var ranking struct {
		IDs []string `json:"ids"`
	}
	if text == "" || json.Unmarshal([]byte(text), &ranking) != nil || len(ranking.IDs) == 0 {
		return RerankResult{}, fmt.Errorf("reranker returned invalid structured output")
	}
	return RerankResult{IDs: ranking.IDs, InputTokens: envelope.Usage.InputTokens, OutputTokens: envelope.Usage.OutputTokens}, nil
}
