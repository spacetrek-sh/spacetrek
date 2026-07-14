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
	Storage       StorageConfig       `yaml:"storage"`
	Security      SecurityConfig      `yaml:"security"`
	Observability ObservabilityConfig `yaml:"observability"`
	Seed         SeedConfig         `yaml:"seed"`
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
	AutoSnapshot       bool          `yaml:"auto_snapshot"`
	ResumeGrace        time.Duration `yaml:"resume_grace"`
	DiskMode           string        `yaml:"disk_mode"`
	MaxChainLength     int           `yaml:"max_chain_length"`
	MaxChainAgeMinutes int           `yaml:"max_chain_age_minutes"`

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
	Network            VMNetworkConfig `yaml:"network"`
}

// VMNetworkConfig defines the VM network topology (bridge, TAP, NAT).
// The guest agent must write /etc/resolv.conf pointing to dns_ip for DNS resolution.
type VMNetworkConfig struct {
	BridgeName string `yaml:"bridge_name"` // Linux bridge name (e.g. br-stk)
	Subnet     string `yaml:"subnet"`      // CIDR subnet (e.g. 10.200.0.0/16)
	GatewayIP  string `yaml:"gateway_ip"`  // Bridge IP acting as VM gateway
	DNSIP      string `yaml:"dns_ip"`      // DNS resolver IP (dnsmasq on bridge gateway)
	IPStart    string `yaml:"ip_start"`    // First allocatable IP
	IPEnd      string `yaml:"ip_end"`      // Last allocatable IP
	EnableNAT  bool   `yaml:"enable_nat"`  // Set up iptables MASQUERADE for internet
}

type LLMConfig struct {
	DefaultProvider string        `yaml:"default_provider"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxRetries      int           `yaml:"max_retries"`
	MaxReactSteps   int           `yaml:"max_react_steps"`
	Gemini          GeminiConfig  `yaml:"gemini"`
}

type GeminiConfig struct {
	APIKey          string `yaml:"api_key"`
	Model           string `yaml:"model"`
	MaxOutputTokens int    `yaml:"max_output_tokens"`
	SystemPrompt    string `yaml:"system_prompt"`
}

type StorageConfig struct {
	Endpoint     string `yaml:"endpoint"`
	Region       string `yaml:"region"`
	AccessKey    string `yaml:"access_key"`
	SecretKey    string `yaml:"secret_key"`
	Bucket       string `yaml:"bucket"`
	UsePathStyle bool   `yaml:"use_path_style"`
}

type SecurityConfig struct {
	JWTSecret          string        `yaml:"jwt_secret"`
	AccessTokenExpiry  time.Duration `yaml:"access_token_expiry"`
	RefreshTokenExpiry time.Duration `yaml:"refresh_token_expiry"`
	MaxTaskDuration    time.Duration `yaml:"max_task_duration"`
}

type SeedConfig struct {
	NamespaceUUID string `yaml:"namespace_uuid"`
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
