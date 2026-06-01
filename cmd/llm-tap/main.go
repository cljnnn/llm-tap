package main

import (
	"log"
	"net/http"
	"os"

	"llm-tap/internal/config"
	"llm-tap/internal/proxy"
	"llm-tap/internal/recorder"
)

func main() {
	configPath := "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	rec := recorder.New(cfg.Logging)
	handler := proxy.NewHandler(cfg, rec)

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Upstream.Timeout,
	}

	log.Printf("llm-tap listening on http://%s", cfg.Server.Listen)
	log.Printf("Forwarding requests to %s", cfg.Upstream.BaseURL)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server stopped: %v", err)
	}
}
