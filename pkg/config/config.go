package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure.
type Config struct {
	Executor ExecutorConfig `yaml:"executor"`
	Network  NetworkConfig  `yaml:"network"`
	Logging  LoggingConfig  `yaml:"logging"`
	Images   ImagesConfig   `yaml:"images"`
	Metrics  MetricsConfig  `yaml:"metrics"`

	// Legacy fields for backward compatibility
	KernelPath      string       `yaml:"kernel_path"`
	InitrdPath      string       `yaml:"initrd_path"`
	RootfsDir       string       `yaml:"rootfs_dir"`
	SocketDir       string       `yaml:"socket_dir"`
	DefaultVCPUs    int          `yaml:"default_vcpus"`
	DefaultMemoryMB int          `yaml:"default_memory_mb"`
	EnableJailer    bool         `yaml:"enable_jailer"`
	Jailer          JailerConfig `yaml:"jailer"`
}

// ExecutorConfig holds executor-specific configuration.
type ExecutorConfig struct {
	Name            string       `yaml:"name"`
	KernelPath      string       `yaml:"kernel_path"`
	InitrdPath      string       `yaml:"initrd_path"`
	RootfsDir       string       `yaml:"rootfs_dir"`
	SocketDir       string       `yaml:"socket_dir"`
	DefaultVCPUs    int          `yaml:"default_vcpus"`
	DefaultMemoryMB int          `yaml:"default_memory_mb"`
	EnableJailer    bool         `yaml:"enable_jailer"`
	Jailer          JailerConfig `yaml:"jailer"`
	InitSystem      string       `yaml:"init_system"`       // "none", "tini", "dumb-init"
	InitGracePeriod int          `yaml:"init_grace_period"` // Grace period in seconds
}

// NetworkConfig holds network configuration.
type NetworkConfig struct {
	BridgeName       string `yaml:"bridge_name"`
	EnableRateLimit  bool   `yaml:"enable_rate_limit"`
	MaxPacketsPerSec int    `yaml:"max_packets_per_sec"`

	// IP allocation settings
	Subnet     string `yaml:"subnet"`      // e.g., "192.168.127.0/24"
	BridgeIP   string `yaml:"bridge_ip"`   // e.g., "192.168.127.1/24"
	IPMode     string `yaml:"ip_mode"`     // "static" or "dhcp"
	NATEnabled bool   `yaml:"nat_enabled"` // Enable masquerading for internet access
}

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	Output string `yaml:"output"`
}

// ImagesConfig holds image preparation configuration.
type ImagesConfig struct {
	CacheDir         string `yaml:"cache_dir"`
	MaxCacheSizeMB   int    `yaml:"max_cache_size_mb"`
	EnableLayerCache bool   `yaml:"enable_layer_cache"`
}

// MetricsConfig holds metrics configuration.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	Format  string `yaml:"format"`
}

// JailerConfig holds jailer-specific configuration.
type JailerConfig struct {
	UID           int    `yaml:"uid"`
	GID           int    `yaml:"gid"`
	ChrootBaseDir string `yaml:"chroot_base_dir"`
	NetNS         string `yaml:"netns"`
}

// LoadConfig loads configuration from a YAML file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Migrate legacy fields to nested structure
	if cfg.KernelPath != "" && cfg.Executor.KernelPath == "" {
		cfg.Executor.KernelPath = cfg.KernelPath
	}
	if cfg.RootfsDir != "" && cfg.Executor.RootfsDir == "" {
		cfg.Executor.RootfsDir = cfg.RootfsDir
	}
	if cfg.InitrdPath != "" && cfg.Executor.InitrdPath == "" {
		cfg.Executor.InitrdPath = cfg.InitrdPath
	}
	if cfg.SocketDir != "" && cfg.Executor.SocketDir == "" {
		cfg.Executor.SocketDir = cfg.SocketDir
	}
	if cfg.DefaultVCPUs > 0 && cfg.Executor.DefaultVCPUs == 0 {
		cfg.Executor.DefaultVCPUs = cfg.DefaultVCPUs
	}
	if cfg.DefaultMemoryMB > 0 && cfg.Executor.DefaultMemoryMB == 0 {
		cfg.Executor.DefaultMemoryMB = cfg.DefaultMemoryMB
	}
	if cfg.EnableJailer && !cfg.Executor.EnableJailer {
		cfg.Executor.EnableJailer = cfg.EnableJailer
	}

	return &cfg, nil
}

// LoadConfigFromEnv loads configuration from the path specified in SWARMCRACKER_CONFIG env var,
// or from the default path if not set.
func LoadConfigFromEnv() (*Config, error) {
	path := GetDefaultConfigPath()
	return LoadConfig(path)
}

// GetDefaultConfigPath returns the default configuration path, or the path from
// SWARMCRACKER_CONFIG environment variable if set.
func GetDefaultConfigPath() string {
	if path := os.Getenv("SWARMCRACKER_CONFIG"); path != "" {
		return path
	}
	return "/etc/swarmcracker/config.yaml"
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	// Check executor config
	if c.Executor.KernelPath == "" && c.KernelPath == "" {
		return fmt.Errorf("kernel_path is required")
	}
	if c.Executor.RootfsDir == "" && c.RootfsDir == "" {
		return fmt.Errorf("rootfs_dir is required")
	}
	if c.Executor.DefaultVCPUs <= 0 && c.DefaultVCPUs <= 0 {
		return fmt.Errorf("default_vcpus must be > 0")
	}
	if c.Executor.DefaultMemoryMB <= 0 && c.DefaultMemoryMB <= 0 {
		return fmt.Errorf("default_memory_mb must be > 0")
	}

	// Validate network config
	if c.Network.BridgeName == "" {
		return fmt.Errorf("network.bridge_name is required")
	}
	if c.Network.EnableRateLimit && c.Network.MaxPacketsPerSec <= 0 {
		return fmt.Errorf("max_packets_per_sec must be > 0 when rate limiting is enabled")
	}

	// Validate jailer config if enabled
	if c.Executor.EnableJailer || c.EnableJailer {
		if err := c.Executor.Jailer.Validate(); err != nil {
			return fmt.Errorf("jailer config invalid: %w", err)
		}
	}

	return nil
}

// SetDefaults sets default values for empty fields.
func (c *Config) SetDefaults() {
	// Set executor defaults
	if c.Executor.KernelPath == "" {
		c.Executor.KernelPath = c.KernelPath
	}
	if c.Executor.RootfsDir == "" {
		c.Executor.RootfsDir = c.RootfsDir
	}
	if c.Executor.SocketDir == "" {
		if c.SocketDir != "" {
			c.Executor.SocketDir = c.SocketDir
		} else {
			c.Executor.SocketDir = "/var/run/firecracker"
		}
	}
	if c.Executor.DefaultVCPUs == 0 {
		c.Executor.DefaultVCPUs = c.DefaultVCPUs
	}
	if c.Executor.DefaultVCPUs == 0 {
		c.Executor.DefaultVCPUs = 1
	}
	if c.Executor.DefaultMemoryMB == 0 {
		c.Executor.DefaultMemoryMB = c.DefaultMemoryMB
	}
	if c.Executor.DefaultMemoryMB == 0 {
		c.Executor.DefaultMemoryMB = 512
	}

	// Set network defaults
	if c.Network.BridgeName == "" {
		c.Network.BridgeName = "swarm-br0"
	}
	if c.Network.Subnet == "" {
		c.Network.Subnet = "192.168.127.0/24"
	}
	if c.Network.BridgeIP == "" {
		c.Network.BridgeIP = "192.168.127.1/24"
	}
	if c.Network.IPMode == "" {
		c.Network.IPMode = "static" // Static IP allocation is simpler
	}
	if !c.Network.NATEnabled {
		c.Network.NATEnabled = true // Enable NAT by default for internet access
	}

	// Set logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.Output == "" {
		c.Logging.Output = "stdout"
	}

	// Set images defaults
	if c.Images.CacheDir == "" {
		c.Images.CacheDir = "/var/cache/swarmcracker"
	}
}

// Merge merges another config into this one, with override taking precedence.
func (c *Config) Merge(other *Config) *Config {
	result := *c

	// Merge executor fields
	if other.Executor.KernelPath != "" {
		result.Executor.KernelPath = other.Executor.KernelPath
	}
	if other.Executor.InitrdPath != "" {
		result.Executor.InitrdPath = other.Executor.InitrdPath
	}
	if other.Executor.RootfsDir != "" {
		result.Executor.RootfsDir = other.Executor.RootfsDir
	}
	if other.Executor.SocketDir != "" {
		result.Executor.SocketDir = other.Executor.SocketDir
	}
	if other.Executor.DefaultVCPUs > 0 {
		result.Executor.DefaultVCPUs = other.Executor.DefaultVCPUs
	}
	if other.Executor.DefaultMemoryMB > 0 {
		result.Executor.DefaultMemoryMB = other.Executor.DefaultMemoryMB
	}
	result.Executor.EnableJailer = other.Executor.EnableJailer

	// Merge network config
	if other.Network.BridgeName != "" {
		result.Network.BridgeName = other.Network.BridgeName
	}
	result.Network.EnableRateLimit = other.Network.EnableRateLimit
	if other.Network.MaxPacketsPerSec > 0 {
		result.Network.MaxPacketsPerSec = other.Network.MaxPacketsPerSec
	}

	return &result
}

// Save saves the configuration to a YAML file.
func (c *Config) Save(path string) error {
	// Create directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// String returns a string representation of the config.
func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{KernelPath: %s, RootfsDir: %s, DefaultVCPUs: %d, DefaultMemoryMB: %d, EnableJailer: %v, BridgeName: %s}",
		c.Executor.KernelPath,
		c.Executor.RootfsDir,
		c.Executor.DefaultVCPUs,
		c.Executor.DefaultMemoryMB,
		c.Executor.EnableJailer,
		c.Network.BridgeName,
	)
}

// Validate validates the jailer configuration.
func (j *JailerConfig) Validate() error {
	if j.UID == 0 {
		return fmt.Errorf("jailer uid is required")
	}
	if j.GID == 0 {
		return fmt.Errorf("jailer gid is required")
	}
	if j.ChrootBaseDir == "" {
		return fmt.Errorf("jailer chroot_base_dir is required")
	}
	return nil
}

// Validate validates the network configuration.
func (n *NetworkConfig) Validate() error {
	if n.BridgeName == "" {
		return fmt.Errorf("bridge_name is required")
	}
	if n.EnableRateLimit && n.MaxPacketsPerSec <= 0 {
		return fmt.Errorf("max_packets_per_sec must be > 0 when rate limiting is enabled")
	}
	if n.IPMode != "" && n.IPMode != "static" && n.IPMode != "dhcp" {
		return fmt.Errorf("ip_mode must be either 'static' or 'dhcp'")
	}
	return nil
}
