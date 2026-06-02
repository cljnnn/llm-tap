package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsExpandNestedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  listen: "127.0.0.1:8080"

upstream:
  base_url: "https://api.openai.com"
  timeout_seconds: 120

logging:
  dir: "./logs"
  pretty_json: true
  expand_nested_json: false
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Logging.ExpandNestedJSON {
		t.Fatalf("Logging.ExpandNestedJSON = true, want false")
	}
}

func TestLoadRejectsInvalidExpandNestedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`upstream:
  base_url: "https://api.openai.com"

logging:
  expand_nested_json: maybe
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load() error = nil, want invalid expand_nested_json error")
	}
}
