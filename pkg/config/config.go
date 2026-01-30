package config

// Config is the top-level configuration structure.
type Config struct {
	Executor ExecutorConfig      `yaml:"executor"`
	Network  NetworkConfig        `yaml:"network"`
	Logging  LoggingConfig        `yaml:"logging"`
	Images   ImagesConfig         `yaml:"images"`
	Metrics  MetricsConfig        `yaml:"metrics"`
}

// ExecutorConfig holds executor-specific configuration.
type ExecutorConfig struct {
	Name           string        `yaml:"name"`
	KernelPath     string        `yaml:"kernel_path"`
	InitrdPath     string        `yaml:"initrd_path"`
	RootfsDir      string        `yaml:"rootfs_dir"`
	SocketDir      string        `yaml:"socket_dir"`
	DefaultVCPUs   int           `yaml:"default_vcpus"`
	DefaultMemoryMB int          `yaml:"default_memory_mb"`
	EnableJailer   bool          `yaml:"enable_jailer"`
	Jailer         JailerConfig  `yaml:"jailer"`
}

// NetworkConfig holds network configuration.
type NetworkConfig struct {
	BridgeName       string `yaml:"bridge_name"`
	EnableRateLimit  bool   `yaml:"enable_rate_limit"`
	MaxPacketsPerSec int    `yaml:"max_packets_per_sec"`
}

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	Output string `yaml:"output"`
}

// ImagesConfig holds image preparation configuration.
type ImagesConfig struct {
	CacheDir        string `yaml:"cache_dir"`
	MaxCacheSizeMB  int    `yaml:"max_cache_size_mb"`
	EnableLayerCache bool  `yaml:"enable_layer_cache"`
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
