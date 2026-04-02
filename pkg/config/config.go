// Package config loads and exposes application configuration from a YAML file.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure matching configs/config.yaml.
type Config struct {
	Environment   string              `yaml:"environment"`
	Server        ServerConfig        `yaml:"server"`
	Database      DatabaseConfig      `yaml:"database"`
	Log           LogConfig           `yaml:"log"`
	VM            VMConfig            `yaml:"vm"`
	LLM           LLMConfig           `yaml:"llm"`
	Security      SecurityConfig      `yaml:"security"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type ServerConfig struct {
	HTTPAddr     string        `yaml:"http_addr"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
}

type DatabaseConfig struct {
	URL            string `yaml:"url"`
	MaxConnections int    `yaml:"max_connections"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type VMConfig struct {
	Provider       string        `yaml:"provider"`
	PoolSize       int           `yaml:"pool_size"`
	MaxVMs         int           `yaml:"max_vms"`
	CPUCount       int           `yaml:"cpu_count"`
	MemoryMB       int           `yaml:"memory_mb"`
	DiskMB         int           `yaml:"disk_mb"`
	NetworkEnabled bool          `yaml:"network_enabled"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	MaxLifetime    time.Duration `yaml:"max_lifetime"`

	Firecracker VMFirecrackerConfig `yaml:"firecracker"`
}

type VMFirecrackerConfig struct {
	BinaryPath         string        `yaml:"binary_path"`
	KernelPath         string        `yaml:"kernel_path"`
	BaseDir            string        `yaml:"base_dir"`
	KernelArgs         string        `yaml:"kernel_args"`
	MacAddress         string        `yaml:"mac_address"`
	SocketTimeout      int           `yaml:"socket_timeout"`
	ShutdownTimeout    int           `yaml:"shutdown_timeout"`
	SMT                bool          `yaml:"smt"`
	EnableMmds         bool          `yaml:"enable_mmds"`
	ExecEnabled        bool          `yaml:"exec_enabled"`
	GuestAgentPort     uint32        `yaml:"guest_agent_port"`
	VsockSocketName    string        `yaml:"vsock_socket_name"`
	CIDMin             uint32        `yaml:"cid_min"`
	CIDMax             uint32        `yaml:"cid_max"`
	DefaultExecTimeout time.Duration `yaml:"default_exec_timeout"`
	MaxStdoutBytes     int           `yaml:"max_stdout_bytes"`
	MaxStderrBytes     int           `yaml:"max_stderr_bytes"`
}

type LLMConfig struct {
	DefaultProvider string        `yaml:"default_provider"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxRetries      int           `yaml:"max_retries"`
}

type SecurityConfig struct {
	JWTSecret          string        `yaml:"jwt_secret"`
	AccessTokenExpiry  time.Duration `yaml:"access_token_expiry"`
	RefreshTokenExpiry time.Duration `yaml:"refresh_token_expiry"`
	MaxTaskDuration    time.Duration `yaml:"max_task_duration"`
}

type ObservabilityConfig struct {
	MetricsEnabled  bool   `yaml:"metrics_enabled"`
	TracingEnabled  bool   `yaml:"tracing_enabled"`
	TracingEndpoint string `yaml:"tracing_endpoint"`
}

// Load reads the YAML config file at the path given by the CONFIG_PATH
// environment variable, falling back to configs/config.yaml.
func Load() (*Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "configs/config.yaml"
	}

	data, err := os.ReadFile(path) // #nosec G304 — path is operator-supplied
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	// Expand ${VAR} / $VAR references so secrets can stay in the environment.
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	return &cfg, nil
}
