package config

import (
	"os"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type Config struct {
	PG      PGConfig      `yaml:"pg"`
	Redis   RedisConfig   `yaml:"redis"`
	Pulsar  PulsarConfig  `yaml:"pulsar"`
	Gateway GatewayConfig `yaml:"gateway"`
}

type PGConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int    `yaml:"max_conns"`
	MaxIdle  int    `yaml:"max_idle"`
}

type RedisConfig struct {
	// Addr is the legacy single-node address. Kept for local/dev YAML compat.
	Addr string `yaml:"addr"`
	// Addrs is the preferred seed list. Cluster mode discovers the rest from
	// one seed; Single mode uses Addrs[0]. Takes precedence over Addr.
	Addrs    []string `yaml:"addrs"`
	Password string   `yaml:"password"`
	// DB is single-node only. Cluster supports DB 0 only; isolation is by
	// key prefix (see repo/routing.go).
	DB int `yaml:"db"`
	// Cluster forces Cluster mode. Set true when the seed list resolves via
	// headless DNS to one entry but the backing Redis is a Cluster.
	Cluster bool `yaml:"cluster"`
}

// ResolveAddrs returns Addrs if non-empty, otherwise falls back to [Addr] so
// older YAML configs keep working.
func (c *RedisConfig) ResolveAddrs() []string {
	if len(c.Addrs) > 0 {
		return c.Addrs
	}
	if c.Addr != "" {
		return []string{c.Addr}
	}
	return nil
}

type PulsarConfig struct {
	URL string `yaml:"url"`
}

type GatewayConfig struct {
	HTTPAddr  string `yaml:"http_addr"`
	JWTSecret string `yaml:"jwt_secret"`
	ID        string `yaml:"id"`        // resolved at runtime from HOSTNAME or UUID if blank
	UploadDir string `yaml:"upload_dir"` // default: /data/uploads
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		// Raised pool defaults after 2026-04-24 benchmark: 20 open + 5 idle
		// saturated by VU=300 and drove P95 to 10s+ on single-pod tests.
		PG:      PGConfig{MaxConns: 50, MaxIdle: 25},
		Gateway: GatewayConfig{HTTPAddr: ":8080"},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	applyEnvOverrides(cfg)

	if cfg.Gateway.UploadDir == "" {
		cfg.Gateway.UploadDir = "/data/uploads"
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("IM_PG_DSN"); v != "" {
		cfg.PG.DSN = v
	}
	if v := os.Getenv("IM_REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("IM_REDIS_ADDRS"); v != "" {
		parts := strings.Split(v, ",")
		addrs := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				addrs = append(addrs, s)
			}
		}
		if len(addrs) > 0 {
			cfg.Redis.Addrs = addrs
		}
	}
	if v := os.Getenv("IM_REDIS_CLUSTER"); v == "true" || v == "1" {
		cfg.Redis.Cluster = true
	}
	if v := os.Getenv("IM_PULSAR_URL"); v != "" {
		cfg.Pulsar.URL = v
	}
	if v := os.Getenv("IM_JWT_SECRET"); v != "" {
		cfg.Gateway.JWTSecret = v
	}
	if v := os.Getenv("IM_GATEWAY_HTTP_ADDR"); v != "" {
		cfg.Gateway.HTTPAddr = v
	}
	if v := os.Getenv("HOSTNAME"); v != "" && cfg.Gateway.ID == "" {
		cfg.Gateway.ID = v
	}
}

// ResolveGatewayID returns cfg.Gateway.ID if set, else generates a random UUID.
// Call once at startup and store the result.
func ResolveGatewayID(cfg *Config) string {
	if cfg.Gateway.ID != "" {
		return cfg.Gateway.ID
	}
	return uuid.New().String()
}
