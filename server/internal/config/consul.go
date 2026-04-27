package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
	"gopkg.in/yaml.v3"
)

// ConsulEnv is the environment selector. The same Go binary speaks to a
// different Consul cluster per env so dev/pre/prod stay isolated.
type ConsulEnv string

const (
	EnvDev  ConsulEnv = "dev"
	EnvPre  ConsulEnv = "pre"
	EnvProd ConsulEnv = "prod"

	// EnvVarEnv selects which Consul cluster + KV path to consult.
	EnvVarEnv = "IM_ENV"
	// EnvVarConsulURL overrides the default Consul URL (handy for local
	// testing against an alternative cluster).
	EnvVarConsulURL = "IM_CONSUL_URL"
	// EnvVarConsulKey overrides the KV path. Empty falls back to the
	// per-env default below.
	EnvVarConsulKey = "IM_CONSUL_KEY"
	// EnvVarConsulToken passes a Consul ACL token if the cluster requires one.
	EnvVarConsulToken = "IM_CONSUL_TOKEN" // #nosec G101 — env var name, not credential
)

// defaultConsulURLByEnv binds each ConsulEnv to its production-grade Consul
// cluster. Override with IM_CONSUL_URL when running against a tunnel or
// alternative cluster.
var defaultConsulURLByEnv = map[ConsulEnv]string{
	EnvDev:  "http://consul-pre.jinqidongli.com",
	EnvPre:  "http://consul-pre.jinqidongli.com",
	EnvProd: "http://consul-prod.jinqidongli.com",
}

// defaultConsulKeyByEnv binds each ConsulEnv to its KV path. The im-go/<env>
// namespace keeps Go config separate from the Java X9-Config-* keys that
// the rest of the platform uses.
var defaultConsulKeyByEnv = map[ConsulEnv]string{
	EnvDev:  "im-go/dev/config.yaml",
	EnvPre:  "im-go/pre/config.yaml",
	EnvProd: "im-go/prod/config.yaml",
}

// LoadFromConsulOrFile resolves config in this priority order:
//  1. If IM_CONSUL_URL is set OR IM_ENV is set → load from Consul.
//  2. Otherwise → load from the supplied YAML path.
//
// The Consul path is keyed per-env (IM_ENV) and overridable
// (IM_CONSUL_URL / IM_CONSUL_KEY / IM_CONSUL_TOKEN). The KV value must be
// the same YAML schema as the local config.yaml file.
//
// Returns the resolved Config and a short human-readable source string
// ("consul:<env>:<key>" or "file:<path>") so the gateway can log where
// the values came from at startup.
func LoadFromConsulOrFile(filePath string) (*Config, string, error) {
	envName := strings.TrimSpace(os.Getenv(EnvVarEnv))
	consulURL := strings.TrimSpace(os.Getenv(EnvVarConsulURL))

	if envName == "" && consulURL == "" {
		cfg, err := Load(filePath)
		if err != nil {
			return nil, "", fmt.Errorf("config load file %q: %w", filePath, err)
		}
		return cfg, "file:" + filePath, nil
	}

	env := ConsulEnv(envName)
	if envName == "" {
		// IM_CONSUL_URL set without IM_ENV → fall back to dev defaults so
		// the lookup still has a key to read.
		env = EnvDev
	}

	if consulURL == "" {
		consulURL = defaultConsulURLByEnv[env]
	}
	if consulURL == "" {
		return nil, "", fmt.Errorf("consul: no URL for env %q (set IM_CONSUL_URL)", env)
	}

	key := strings.TrimSpace(os.Getenv(EnvVarConsulKey))
	if key == "" {
		key = defaultConsulKeyByEnv[env]
	}
	if key == "" {
		return nil, "", fmt.Errorf("consul: no KV key for env %q (set IM_CONSUL_KEY)", env)
	}

	cfg, err := loadFromConsul(consulURL, key, os.Getenv(EnvVarConsulToken))
	if err != nil {
		return nil, "", fmt.Errorf("config load consul %s/%s: %w", consulURL, key, err)
	}
	applyDefaults(cfg)
	// Same env override path as file-loaded configs (IM_JWT_SECRET,
	// IM_REDIS_*, IM_PULSAR_URL, IM_GATEWAY_HTTP_ADDR, HOSTNAME). K8s
	// deployments inject secrets via env, not Consul KV — so this hook
	// must run regardless of source.
	applyEnvOverrides(cfg)
	return cfg, fmt.Sprintf("consul:%s:%s", env, key), nil
}

// loadFromConsul fetches a single KV by path and parses it as YAML. The
// HTTP timeout / TLS / token are configured on the underlying Consul
// client (defaults are fine for the on-prem clusters).
func loadFromConsul(consulURL, key, token string) (*Config, error) {
	clientCfg := consulapi.DefaultConfig()
	clientCfg.Address = consulURL
	if token != "" {
		clientCfg.Token = token
	}
	client, err := consulapi.NewClient(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("consul client: %w", err)
	}
	pair, _, err := client.KV().Get(key, nil)
	if err != nil {
		return nil, fmt.Errorf("consul kv get %q: %w", key, err)
	}
	if pair == nil || len(pair.Value) == 0 {
		return nil, fmt.Errorf("consul kv %q not found or empty", key)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(pair.Value, cfg); err != nil {
		return nil, fmt.Errorf("consul kv yaml unmarshal: %w", err)
	}
	return cfg, nil
}

// applyDefaults mirrors the defaults that the file-based Load() sets so a
// Consul-sourced config is functionally equivalent. Centralised here so
// new defaults need to be added once, not twice.
func applyDefaults(cfg *Config) {
	if cfg.PG.MaxConns == 0 {
		cfg.PG.MaxConns = 300
	}
	if cfg.PG.MaxIdle == 0 {
		cfg.PG.MaxIdle = 30
	}
	if cfg.PG.ConnMaxLifeSec == 0 {
		cfg.PG.ConnMaxLifeSec = 600
	}
	if cfg.PG.ConnMaxIdleSec == 0 {
		cfg.PG.ConnMaxIdleSec = 300
	}
	if cfg.Gateway.HTTPAddr == "" {
		cfg.Gateway.HTTPAddr = ":8080"
	}
	if cfg.Gateway.UploadDir == "" {
		cfg.Gateway.UploadDir = "/data/uploads"
	}
}

// ErrConsulKeyMissing is returned when the env says "consult Consul" but
// the KV key isn't present. Callers can detect it and fall back to local
// YAML if they prefer.
var ErrConsulKeyMissing = errors.New("consul kv key missing")
