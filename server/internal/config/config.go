package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	PG     PGConfig     `yaml:"pg"`
	Redis  RedisConfig  `yaml:"redis"`
	Pulsar PulsarConfig `yaml:"pulsar"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		PG: PGConfig{MaxConns: 20},
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
}
