package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	content := []byte(`
pg:
  dsn: "postgres://user:pass@localhost:5432/im?sslmode=disable"
redis:
  addr: "localhost:6379"
pulsar:
  url: "pulsar://localhost:6650"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.PG.DSN != "postgres://user:pass@localhost:5432/im?sslmode=disable" {
		t.Errorf("PG.DSN = %q, want postgres://...", cfg.PG.DSN)
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("Redis.Addr = %q, want localhost:6379", cfg.Redis.Addr)
	}
	if cfg.Pulsar.URL != "pulsar://localhost:6650" {
		t.Errorf("Pulsar.URL = %q, want pulsar://localhost:6650", cfg.Pulsar.URL)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	content := []byte(`
pg:
  dsn: "postgres://default@localhost/im"
redis:
  addr: "localhost:6379"
pulsar:
  url: "pulsar://localhost:6650"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("IM_PG_DSN", "postgres://override@db:5432/im")

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.PG.DSN != "postgres://override@db:5432/im" {
		t.Errorf("PG.DSN = %q, want override DSN", cfg.PG.DSN)
	}
}
