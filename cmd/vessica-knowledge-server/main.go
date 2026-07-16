package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vessica-labs/vessica-knowledge-server/knowledge"
	"github.com/vessica-labs/vessica-knowledge-server/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var store knowledge.Store
	var err error
	databaseURL := strings.TrimSpace(os.Getenv("VES_KNOWLEDGE_DATABASE_URL"))
	hosted := databaseURL != ""
	if databaseURL != "" {
		store, err = knowledge.OpenPostgres(databaseURL)
	} else {
		path := os.Getenv("KNOWLEDGE_SQLITE_PATH")
		if path == "" {
			path = "knowledge.db"
		}
		store, err = knowledge.OpenSQLite(path)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	var embedder knowledge.Embedder
	if key := os.Getenv("EMBEDDING_API_KEY"); key != "" {
		embedder = &knowledge.HTTPEmbedder{APIKey: key, BaseURL: os.Getenv("EMBEDDING_BASE_URL"), ModelName: defaultValue(os.Getenv("EMBEDDING_MODEL"), "text-embedding-3-small")}
	}
	if hosted && strings.TrimSpace(os.Getenv("KNOWLEDGE_API_TOKEN")) == "" {
		log.Fatal("KNOWLEDGE_API_TOKEN is required in hosted mode")
	}
	if hosted && strings.TrimSpace(os.Getenv("KNOWLEDGE_EXPORT_TOKEN")) == "" {
		log.Fatal("KNOWLEDGE_EXPORT_TOKEN is required in hosted mode")
	}
	if hosted && strings.TrimSpace(os.Getenv("KNOWLEDGE_WORKSPACE_ID")) == "" {
		log.Fatal("KNOWLEDGE_WORKSPACE_ID is required in hosted mode")
	}
	svc := knowledge.NewService(store, embedder)
	svc.StartEmbeddingWorker(ctx)
	srv := &http.Server{Addr: ":" + defaultValue(os.Getenv("PORT"), "8080"), Handler: (&server.Server{Service: svc, Token: os.Getenv("KNOWLEDGE_API_TOKEN"), ExportToken: os.Getenv("KNOWLEDGE_EXPORT_TOKEN"), DefaultWorkspace: os.Getenv("KNOWLEDGE_WORKSPACE_ID")}).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("vessica knowledge server listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func defaultValue(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
