package config

import (
	"os"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type Config struct {
	PG            PGConfig            `yaml:"pg"`
	Redis         RedisConfig         `yaml:"redis"`
	Pulsar        PulsarConfig        `yaml:"pulsar"`
	Gateway       GatewayConfig       `yaml:"gateway"`
	Observability ObservabilityConfig `yaml:"observability"`
}

// ObservabilityConfig points the OTel SDK at a collector.
//
// Endpoint is OTLP/gRPC host:port (no scheme). When empty the SDK falls back
// to the OTEL_EXPORTER_OTLP_ENDPOINT env var (default localhost:4317), which
// is convenient for local dev but throws "connection refused" in pre/prod
// where the collector is named (e.g. jaeger-cses-pre-collector.jaeger-cses
// .svc.cluster.local:4317). Disabled short-circuits Init to a noop.
//
// SampleRatio is a parent-based head sampler ratio (0.0..1.0). Zero means
// "let Init pick the default" (1.0 — sample everything).
type ObservabilityConfig struct {
	Endpoint    string  `yaml:"endpoint"`
	Disabled    bool    `yaml:"disabled"`
	SampleRatio float64 `yaml:"sample_ratio"`
}

// PGConfig mirrors the Java HikariCP knobs so the two services use
// comparable pool sizes when they share the same Postgres instance.
//
// Java (reference):
//
//	maximum-pool-size: 300
//	minimum-idle: 1
//	idle-timeout: 300000        # 300s
//	max-lifetime: 600000        # 600s
//	connection-timeout: 30000   # handled at context level on the Go side
type PGConfig struct {
	DSN            string `yaml:"dsn"`
	MaxConns       int    `yaml:"max_conns"`       // ~= HikariCP maximum-pool-size
	MaxIdle        int    `yaml:"max_idle"`        // Go has no minimum-idle; set to ~10% of MaxConns
	ConnMaxLifeSec int    `yaml:"conn_max_life_s"` // ~= HikariCP max-lifetime, seconds
	ConnMaxIdleSec int    `yaml:"conn_max_idle_s"` // ~= HikariCP idle-timeout, seconds
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
	ID        string `yaml:"id"`         // resolved at runtime from HOSTNAME or UUID if blank
	UploadDir string `yaml:"upload_dir"` // default: /data/uploads
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		// Align defaults with the Java HikariCP settings used by sibling cses
		// services on the same PG instance (max=300 / max-life=600s /
		// idle-timeout=300s). Benchmarks showed 20/50 both saturated under
		// modest VU counts.
		PG: PGConfig{
			MaxConns:       300,
			MaxIdle:        30,
			ConnMaxLifeSec: 600,
			ConnMaxIdleSec: 300,
		},
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
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" && cfg.Observability.Endpoint == "" {
		cfg.Observability.Endpoint = v
	}
	if v := os.Getenv("OTEL_DISABLED"); v == "true" || v == "1" {
		cfg.Observability.Disabled = true
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
