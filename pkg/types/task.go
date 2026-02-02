// Package types contains shared data structures used across SwarmCracker.
package types

import "context"

// Task represents a SwarmKit task (simplified).
type Task struct {
	ID          string
	ServiceID   string
	NodeID      string
	Spec        TaskSpec
	Status      TaskStatus
	Networks    []NetworkAttachment
	Annotations map[string]string
}

// TaskSpec defines the task specification.
type TaskSpec struct {
	Runtime   interface{} // *Container
	Resources ResourceRequirements
	Restart   RestartPolicy
	Placement Placement
}

// Container specifies container configuration.
type Container struct {
	Image   string
	Command []string
	Args    []string
	Env     []string
	Mounts  []Mount
}

// Mount specifies a volume mount.
type Mount struct {
	Target   string
	Source   string
	ReadOnly bool
}

// ResourceRequirements specifies resource limits.
type ResourceRequirements struct {
	Limits       *Resources
	Reservations *Resources
}

// Resources specifies resource limits.
type Resources struct {
	NanoCPUs    int64
	MemoryBytes int64
}

// RestartPolicy specifies restart behavior.
type RestartPolicy struct {
	Condition   RestartPolicy_Condition
	MaxAttempts uint64
}

// RestartPolicy_Condition specifies when to restart.
type RestartPolicy_Condition int

const (
	RestartPolicy_ANY        RestartPolicy_Condition = 0
	RestartPolicy_NONE       RestartPolicy_Condition = 1
	RestartPolicy_ON_FAILURE RestartPolicy_Condition = 2
)

// Placement specifies placement constraints.
type Placement struct {
	Constraints []string
}

// TaskStatus represents task status.
type TaskStatus struct {
	State         TaskState
	RuntimeStatus interface{}
	Timestamp     int64
	Message       string
	Err           error
}

// TaskState represents the current state of a task.
type TaskState int

const (
	TaskState_NEW       TaskState = 0
	TaskState_PENDING   TaskState = 1
	TaskState_ASSIGNED  TaskState = 2
	TaskState_ACCEPTED  TaskState = 3
	TaskState_PREPARING TaskState = 4
	TaskState_STARTING  TaskState = 5
	TaskState_RUNNING   TaskState = 6
	TaskState_COMPLETE  TaskState = 7
	TaskState_FAILED    TaskState = 8
	TaskState_REJECTED  TaskState = 9
	TaskState_REMOVE    TaskState = 10
	TaskState_ORPHANED  TaskState = 11
)

// NetworkAttachment represents network attachment.
type NetworkAttachment struct {
	Network   Network
	Addresses []string
}

// Network represents a network.
type Network struct {
	ID   string
	Spec NetworkSpec
}

// NetworkSpec specifies network configuration.
type NetworkSpec struct {
	DriverConfig *DriverConfig
}

// DriverConfig specifies network driver configuration.
type DriverConfig struct {
	Bridge *BridgeConfig
}

// BridgeConfig specifies bridge configuration.
type BridgeConfig struct {
	Name string
}

// NetworkConfig holds network-related settings (for configuration).
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

// Interfaces for the executor components

// VMMManager manages Firecracker VM lifecycle.
type VMMManager interface {
	Start(ctx context.Context, task *Task, config interface{}) error
	Stop(ctx context.Context, task *Task) error
	Wait(ctx context.Context, task *Task) (*TaskStatus, error)
	Describe(ctx context.Context, task *Task) (*TaskStatus, error)
	Remove(ctx context.Context, task *Task) error
}

// TaskTranslator converts SwarmKit tasks to Firecracker configs.
type TaskTranslator interface {
	Translate(task *Task) (interface{}, error)
}

// ImagePreparer prepares OCI images as root filesystems.
type ImagePreparer interface {
	Prepare(ctx context.Context, task *Task) error
}

// NetworkManager manages VM networking.
type NetworkManager interface {
	PrepareNetwork(ctx context.Context, task *Task) error
	CleanupNetwork(ctx context.Context, task *Task) error
	GetTapIP(taskID string) (string, error) // Get allocated IP for task
}
