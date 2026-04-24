package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestRedisResolveAddrs(t *testing.T) {
	tests := []struct {
		name string
		cfg  RedisConfig
		want []string
	}{
		{
			name: "Addrs wins over Addr",
			cfg:  RedisConfig{Addr: "legacy:6379", Addrs: []string{"a:6379", "b:6379"}},
			want: []string{"a:6379", "b:6379"},
		},
		{
			name: "Addr fallback when Addrs empty",
			cfg:  RedisConfig{Addr: "legacy:6379"},
			want: []string{"legacy:6379"},
		},
		{
			name: "empty when both unset",
			cfg:  RedisConfig{},
			want: nil,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ResolveAddrs()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResolveAddrs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadRedisClusterEnvOverride(t *testing.T) {
	content := []byte(`redis:
  addr: "ignored:6379"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("IM_REDIS_ADDRS", "a:6379, b:6379,  c:6379")
	t.Setenv("IM_REDIS_CLUSTER", "true")

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	wantAddrs := []string{"a:6379", "b:6379", "c:6379"}
	if !reflect.DeepEqual(cfg.Redis.Addrs, wantAddrs) {
		t.Errorf("Redis.Addrs = %v, want %v", cfg.Redis.Addrs, wantAddrs)
	}
	if !cfg.Redis.Cluster {
		t.Error("Redis.Cluster = false, want true")
	}
	// ResolveAddrs should prefer the env-provided Addrs over legacy Addr.
	got := cfg.Redis.ResolveAddrs()
	if !reflect.DeepEqual(got, wantAddrs) {
		t.Errorf("ResolveAddrs() = %v, want %v", got, wantAddrs)
	}
}

func TestLoadRedisClusterEnvOverrideFalseValues(t *testing.T) {
	content := []byte(`redis:
  addr: "localhost:6379"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Non-truthy values must NOT flip Cluster to true.
	t.Setenv("IM_REDIS_CLUSTER", "no")

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Redis.Cluster {
		t.Error("Redis.Cluster = true, want false for IM_REDIS_CLUSTER=no")
	}
}
