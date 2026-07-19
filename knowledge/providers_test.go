package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPEmbedderValidatesProviderResponsesWithoutLeakingBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "valid", status: http.StatusOK, body: `{"data":[{"embedding":[1,0.5,-1]}]}`},
		{name: "empty", status: http.StatusOK, body: `{"data":[{"embedding":[]}]}`, want: "empty vector"},
		{name: "malformed", status: http.StatusOK, body: `{"data":`, want: "unexpected end"},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `secret provider diagnostic`, want: "failed (429)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatalf("unexpected request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			embedder := &HTTPEmbedder{APIKey: "test-key", BaseURL: server.URL, ModelName: "text-embedding-3-small"}
			vector, err := embedder.Embed(context.Background(), "query")
			if test.want == "" {
				if err != nil || len(vector) != 3 {
					t.Fatalf("vector=%v err=%v", vector, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret provider diagnostic") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHTTPRerankerUsesStrictResponsesContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer rerank-key" {
			t.Fatalf("unexpected request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["store"] != false || request["model"] != "gpt-5.6-luna" {
			t.Fatalf("request=%#v", request)
		}
		text, _ := request["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Fatalf("format=%#v", format)
		}
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"{\"ids\":[\"mem_b\",\"mem_a\"]}"}]}],"usage":{"input_tokens":21,"output_tokens":6}}`))
	}))
	defer server.Close()
	reranker := &HTTPReranker{APIKey: "rerank-key", BaseURL: server.URL, ModelName: "gpt-5.6-luna"}
	result, err := reranker.Rerank(context.Background(), "which preference", []Memory{{ID: "mem_a", Content: "one"}, {ID: "mem_b", Content: "two"}})
	if err != nil || strings.Join(result.IDs, ",") != "mem_b,mem_a" || result.InputTokens != 21 || result.OutputTokens != 6 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestHTTPRerankerFailureModesFallbackCleanly(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		delay  time.Duration
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, body: `provider-private-message`},
		{name: "schema violation", status: http.StatusOK, body: `{"output":[{"content":[{"type":"output_text","text":"{}"}]}]}`},
		{name: "refusal", status: http.StatusOK, body: `{"output":[{"content":[{"type":"refusal","text":"no"}]}]}`},
		{name: "timeout", status: http.StatusOK, body: `{}`, delay: 50 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(test.delay)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := &http.Client{}
			reranker := &HTTPReranker{APIKey: "key", BaseURL: server.URL, ModelName: "model", Client: client}
			ctx := context.Background()
			if test.delay > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
				defer cancel()
			}
			_, err := reranker.Rerank(ctx, "query", []Memory{{ID: "a"}})
			if err == nil || strings.Contains(err.Error(), "provider-private-message") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
