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

type HTTPEmbedder struct {
	APIKey    string
	BaseURL   string
	ModelName string
	Client    *http.Client
}

func (e *HTTPEmbedder) Provider() string { return "openai_compatible" }
func (e *HTTPEmbedder) Model() string    { return e.ModelName }
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if e.APIKey == "" {
		return nil, fmt.Errorf("embedding API key required")
	}
	base := strings.TrimRight(e.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body, _ := json.Marshal(map[string]any{"model": e.ModelName, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding provider returned no data")
	}
	return out.Data[0].Embedding, nil
}
