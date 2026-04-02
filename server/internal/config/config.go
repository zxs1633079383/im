package config

import (
	"os"

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
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type PulsarConfig struct {
	URL string `yaml:"url"`
}

type GatewayConfig struct {
	HTTPAddr  string `yaml:"http_addr"`
	JWTSecret string `yaml:"jwt_secret"`
	ID        string `yaml:"id"` // resolved at runtime from HOSTNAME or UUID if blank
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		PG:      PGConfig{MaxConns: 20},
		Gateway: GatewayConfig{HTTPAddr: ":8080"},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	applyEnvOverrides(cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("IM_PG_DSN"); v != "" {
		cfg.PG.DSN = v
	}
	if v := os.Getenv("IM_REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
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
