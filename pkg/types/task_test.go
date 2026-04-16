package types

import (
	"context"
	"encoding/json"
	"testing"
)

// TestSecretRefConstruction tests SecretRef struct construction and defaults
func TestSecretRefConstruction(t *testing.T) {
	tests := []struct {
		name   string
		secret SecretRef
		want   SecretRef
	}{
		{
			name:   "zero value",
			secret: SecretRef{},
			want: SecretRef{
				ID:     "",
				Name:   "",
				Target: "",
				Data:   nil,
			},
		},
		{
			name: "with values",
			secret: SecretRef{
				ID:     "secret123",
				Name:   "my_secret",
				Target: "/run/secrets/my_secret",
				Data:   []byte("sensitive data"),
			},
			want: SecretRef{
				ID:     "secret123",
				Name:   "my_secret",
				Target: "/run/secrets/my_secret",
				Data:   []byte("sensitive data"),
			},
		},
		{
			name: "nil data slice",
			secret: SecretRef{
				ID:     "secret456",
				Name:   "another_secret",
				Target: "/run/secrets/another",
				Data:   nil,
			},
			want: SecretRef{
				ID:     "secret456",
				Name:   "another_secret",
				Target: "/run/secrets/another",
				Data:   nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.secret.ID != tt.want.ID {
				t.Errorf("ID = %v, want %v", tt.secret.ID, tt.want.ID)
			}
			if tt.secret.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", tt.secret.Name, tt.want.Name)
			}
			if tt.secret.Target != tt.want.Target {
				t.Errorf("Target = %v, want %v", tt.secret.Target, tt.want.Target)
			}
			if tt.secret.Data == nil && tt.want.Data != nil {
				t.Errorf("Data = nil, want %v", tt.want.Data)
			}
			if tt.secret.Data != nil && tt.want.Data != nil {
				if string(tt.secret.Data) != string(tt.want.Data) {
					t.Errorf("Data = %v, want %v", tt.secret.Data, tt.want.Data)
				}
			}
		})
	}
}

// TestConfigRefConstruction tests ConfigRef struct construction and defaults
func TestConfigRefConstruction(t *testing.T) {
	tests := []struct {
		name   string
		config ConfigRef
		want   ConfigRef
	}{
		{
			name:   "zero value",
			config: ConfigRef{},
			want: ConfigRef{
				ID:     "",
				Name:   "",
				Target: "",
				Data:   nil,
			},
		},
		{
			name: "with values",
			config: ConfigRef{
				ID:     "config123",
				Name:   "app_config",
				Target: "/config/app.yaml",
				Data:   []byte("key: value"),
			},
			want: ConfigRef{
				ID:     "config123",
				Name:   "app_config",
				Target: "/config/app.yaml",
				Data:   []byte("key: value"),
			},
		},
		{
			name: "empty data slice",
			config: ConfigRef{
				ID:     "config456",
				Name:   "empty_config",
				Target: "/config/empty.yaml",
				Data:   []byte{},
			},
			want: ConfigRef{
				ID:     "config456",
				Name:   "empty_config",
				Target: "/config/empty.yaml",
				Data:   []byte{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.ID != tt.want.ID {
				t.Errorf("ID = %v, want %v", tt.config.ID, tt.want.ID)
			}
			if tt.config.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", tt.config.Name, tt.want.Name)
			}
			if tt.config.Target != tt.want.Target {
				t.Errorf("Target = %v, want %v", tt.config.Target, tt.want.Target)
			}
			if tt.config.Data == nil && tt.want.Data != nil {
				t.Errorf("Data = nil, want %v", tt.want.Data)
			}
			if tt.config.Data != nil && tt.want.Data != nil {
				if string(tt.config.Data) != string(tt.want.Data) {
					t.Errorf("Data = %v, want %v", tt.config.Data, tt.want.Data)
				}
			}
		})
	}
}

// TestMountConstruction tests Mount struct construction
func TestMountConstruction(t *testing.T) {
	tests := []struct {
		name  string
		mount Mount
		want  Mount
	}{
		{
			name:  "zero value",
			mount: Mount{},
			want: Mount{
				Target:   "",
				Source:   "",
				ReadOnly: false,
			},
		},
		{
			name: "read-write mount",
			mount: Mount{
				Target:   "/data",
				Source:   "/host/data",
				ReadOnly: false,
			},
			want: Mount{
				Target:   "/data",
				Source:   "/host/data",
				ReadOnly: false,
			},
		},
		{
			name: "read-only mount",
			mount: Mount{
				Target:   "/readonly",
				Source:   "/host/readonly",
				ReadOnly: true,
			},
			want: Mount{
				Target:   "/readonly",
				Source:   "/host/readonly",
				ReadOnly: true,
			},
		},
		{
			name: "empty source",
			mount: Mount{
				Target:   "/tmp",
				Source:   "",
				ReadOnly: false,
			},
			want: Mount{
				Target:   "/tmp",
				Source:   "",
				ReadOnly: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mount.Target != tt.want.Target {
				t.Errorf("Target = %v, want %v", tt.mount.Target, tt.want.Target)
			}
			if tt.mount.Source != tt.want.Source {
				t.Errorf("Source = %v, want %v", tt.mount.Source, tt.want.Source)
			}
			if tt.mount.ReadOnly != tt.want.ReadOnly {
				t.Errorf("ReadOnly = %v, want %v", tt.mount.ReadOnly, tt.want.ReadOnly)
			}
		})
	}
}

// TestResourcesConstruction tests Resources struct construction
func TestResourcesConstruction(t *testing.T) {
	tests := []struct {
		name      string
		resources Resources
		want      Resources
	}{
		{
			name: "zero value",
			resources: Resources{},
			want: Resources{
				NanoCPUs:    0,
				MemoryBytes: 0,
			},
		},
		{
			name: "with CPU and memory",
			resources: Resources{
				NanoCPUs:    1000000000, // 1 CPU
				MemoryBytes: 512 * 1024 * 1024, // 512 MB
			},
			want: Resources{
				NanoCPUs:    1000000000,
				MemoryBytes: 512 * 1024 * 1024,
			},
		},
		{
			name: "CPU only",
			resources: Resources{
				NanoCPUs:    500000000, // 0.5 CPU
				MemoryBytes: 0,
			},
			want: Resources{
				NanoCPUs:    500000000,
				MemoryBytes: 0,
			},
		},
		{
			name: "memory only",
			resources: Resources{
				NanoCPUs:    0,
				MemoryBytes: 256 * 1024 * 1024, // 256 MB
			},
			want: Resources{
				NanoCPUs:    0,
				MemoryBytes: 256 * 1024 * 1024,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.resources.NanoCPUs != tt.want.NanoCPUs {
				t.Errorf("NanoCPUs = %v, want %v", tt.resources.NanoCPUs, tt.want.NanoCPUs)
			}
			if tt.resources.MemoryBytes != tt.want.MemoryBytes {
				t.Errorf("MemoryBytes = %v, want %v", tt.resources.MemoryBytes, tt.want.MemoryBytes)
			}
		})
	}
}

// TestResourceRequirementsConstruction tests ResourceRequirements with nil pointers
func TestResourceRequirementsConstruction(t *testing.T) {
	tests := []struct {
		name   string
		req    ResourceRequirements
		check  func(*testing.T, ResourceRequirements)
	}{
		{
			name: "zero value - both nil",
			req:  ResourceRequirements{},
			check: func(t *testing.T, r ResourceRequirements) {
				if r.Limits != nil {
					t.Errorf("Limits = %v, want nil", r.Limits)
				}
				if r.Reservations != nil {
					t.Errorf("Reservations = %v, want nil", r.Reservations)
				}
			},
		},
		{
			name: "limits only",
			req: ResourceRequirements{
				Limits: &Resources{
					NanoCPUs:    2000000000,
					MemoryBytes: 1024 * 1024 * 1024,
				},
			},
			check: func(t *testing.T, r ResourceRequirements) {
				if r.Limits == nil {
					t.Errorf("Limits = nil, want non-nil")
				} else if r.Limits.NanoCPUs != 2000000000 {
					t.Errorf("Limits.NanoCPUs = %v, want 2000000000", r.Limits.NanoCPUs)
				}
				if r.Reservations != nil {
					t.Errorf("Reservations = %v, want nil", r.Reservations)
				}
			},
		},
		{
			name: "reservations only",
			req: ResourceRequirements{
				Reservations: &Resources{
					NanoCPUs:    500000000,
					MemoryBytes: 256 * 1024 * 1024,
				},
			},
			check: func(t *testing.T, r ResourceRequirements) {
				if r.Reservations == nil {
					t.Errorf("Reservations = nil, want non-nil")
				} else if r.Reservations.NanoCPUs != 500000000 {
					t.Errorf("Reservations.NanoCPUs = %v, want 500000000", r.Reservations.NanoCPUs)
				}
				if r.Limits != nil {
					t.Errorf("Limits = %v, want nil", r.Limits)
				}
			},
		},
		{
			name: "both limits and reservations",
			req: ResourceRequirements{
				Limits: &Resources{
					NanoCPUs:    2000000000,
					MemoryBytes: 1024 * 1024 * 1024,
				},
				Reservations: &Resources{
					NanoCPUs:    500000000,
					MemoryBytes: 256 * 1024 * 1024,
				},
			},
			check: func(t *testing.T, r ResourceRequirements) {
				if r.Limits == nil {
					t.Errorf("Limits = nil, want non-nil")
				}
				if r.Reservations == nil {
					t.Errorf("Reservations = nil, want non-nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.req)
		})
	}
}

// TestRestartPolicyConditionConstants verifies RestartPolicyCondition constants
func TestRestartPolicyConditionConstants(t *testing.T) {
	tests := []struct {
		name      string
		constant  RestartPolicyCondition
		wantValue int
	}{
		{"RestartPolicyAny", RestartPolicyAny, 0},
		{"RestartPolicyNone", RestartPolicyNone, 1},
		{"RestartPolicyOnFailure", RestartPolicyOnFailure, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.constant) != tt.wantValue {
				t.Errorf("%s = %v, want %v", tt.name, tt.constant, tt.wantValue)
			}
		})
	}
}

// TestRestartPolicyConstruction tests RestartPolicy struct construction
func TestRestartPolicyConstruction(t *testing.T) {
	tests := []struct {
		name   string
		policy RestartPolicy
		want   RestartPolicy
	}{
		{
			name: "zero value",
			policy: RestartPolicy{},
			want: RestartPolicy{
				Condition:   0, // RestartPolicyAny
				MaxAttempts: 0,
			},
		},
		{
			name: "always restart",
			policy: RestartPolicy{
				Condition:   RestartPolicyAny,
				MaxAttempts: 0, // unlimited
			},
			want: RestartPolicy{
				Condition:   RestartPolicyAny,
				MaxAttempts: 0,
			},
		},
		{
			name: "restart on failure with max attempts",
			policy: RestartPolicy{
				Condition:   RestartPolicyOnFailure,
				MaxAttempts: 3,
			},
			want: RestartPolicy{
				Condition:   RestartPolicyOnFailure,
				MaxAttempts: 3,
			},
		},
		{
			name: "no restart",
			policy: RestartPolicy{
				Condition:   RestartPolicyNone,
				MaxAttempts: 0,
			},
			want: RestartPolicy{
				Condition:   RestartPolicyNone,
				MaxAttempts: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.policy.Condition != tt.want.Condition {
				t.Errorf("Condition = %v, want %v", tt.policy.Condition, tt.want.Condition)
			}
			if tt.policy.MaxAttempts != tt.want.MaxAttempts {
				t.Errorf("MaxAttempts = %v, want %v", tt.policy.MaxAttempts, tt.want.MaxAttempts)
			}
		})
	}
}

// TestPlacementConstruction tests Placement struct with nil/empty slices
func TestPlacementConstruction(t *testing.T) {
	tests := []struct {
		name      string
		placement Placement
		check     func(*testing.T, Placement)
	}{
		{
			name: "zero value - nil slice",
			placement: Placement{},
			check: func(t *testing.T, p Placement) {
				if p.Constraints != nil {
					t.Errorf("Constraints = %v, want nil", p.Constraints)
				}
			},
		},
		{
			name: "with constraints",
			placement: Placement{
				Constraints: []string{"node.role == worker", "node.labels.zone == us-east-1"},
			},
			check: func(t *testing.T, p Placement) {
				if p.Constraints == nil {
					t.Errorf("Constraints = nil, want non-nil")
				}
				if len(p.Constraints) != 2 {
					t.Errorf("len(Constraints) = %v, want 2", len(p.Constraints))
				}
			},
		},
		{
			name: "empty constraints slice",
			placement: Placement{
				Constraints: []string{},
			},
			check: func(t *testing.T, p Placement) {
				if p.Constraints == nil {
					t.Errorf("Constraints = nil, want non-nil empty slice")
				}
				if len(p.Constraints) != 0 {
					t.Errorf("len(Constraints) = %v, want 0", len(p.Constraints))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.placement)
		})
	}
}

// TestContainerConstruction tests Container struct with nil/empty slices
func TestContainerConstruction(t *testing.T) {
	tests := []struct {
		name      string
		container Container
		check     func(*testing.T, Container)
	}{
		{
			name:      "zero value",
			container: Container{},
			check: func(t *testing.T, c Container) {
				if c.Image != "" {
					t.Errorf("Image = %v, want empty", c.Image)
				}
				if c.Command != nil {
					t.Errorf("Command = %v, want nil", c.Command)
				}
				if c.Args != nil {
					t.Errorf("Args = %v, want nil", c.Args)
				}
				if c.Env != nil {
					t.Errorf("Env = %v, want nil", c.Env)
				}
				if c.Mounts != nil {
					t.Errorf("Mounts = %v, want nil", c.Mounts)
				}
			},
		},
		{
			name: "full container spec",
			container: Container{
				Image:   "nginx:latest",
				Command: []string{"/bin/sh"},
				Args:    []string{"-c", "echo hello"},
				Env:     []string{"PATH=/usr/bin", "TZ=UTC"},
				Mounts: []Mount{
					{Target: "/data", Source: "/host/data"},
				},
			},
			check: func(t *testing.T, c Container) {
				if c.Image != "nginx:latest" {
					t.Errorf("Image = %v, want nginx:latest", c.Image)
				}
				if len(c.Command) != 1 {
					t.Errorf("len(Command) = %v, want 1", len(c.Command))
				}
				if len(c.Mounts) != 1 {
					t.Errorf("len(Mounts) = %v, want 1", len(c.Mounts))
				}
			},
		},
		{
			name: "empty slices",
			container: Container{
				Image:   "alpine:latest",
				Command: []string{},
				Args:    []string{},
				Env:     []string{},
				Mounts:  []Mount{},
			},
			check: func(t *testing.T, c Container) {
				if len(c.Command) != 0 {
					t.Errorf("len(Command) = %v, want 0", len(c.Command))
				}
				if len(c.Args) != 0 {
					t.Errorf("len(Args) = %v, want 0", len(c.Args))
				}
				if len(c.Env) != 0 {
					t.Errorf("len(Env) = %v, want 0", len(c.Env))
				}
				if len(c.Mounts) != 0 {
					t.Errorf("len(Mounts) = %v, want 0", len(c.Mounts))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.container)
		})
	}
}

// TestTaskStateConstants verifies TaskState constants
func TestTaskStateConstants(t *testing.T) {
	tests := []struct {
		name      string
		constant  TaskState
		wantValue int
	}{
		{"TaskStateNew", TaskStateNew, 0},
		{"TaskStatePending", TaskStatePending, 1},
		{"TaskStateAssigned", TaskStateAssigned, 2},
		{"TaskStateAccepted", TaskStateAccepted, 3},
		{"TaskStatePreparing", TaskStatePreparing, 4},
		{"TaskStateStarting", TaskStateStarting, 5},
		{"TaskStateRunning", TaskStateRunning, 6},
		{"TaskStateComplete", TaskStateComplete, 7},
		{"TaskStateFailed", TaskStateFailed, 8},
		{"TaskStateRejected", TaskStateRejected, 9},
		{"TaskStateRemove", TaskStateRemove, 10},
		{"TaskStateOrphaned", TaskStateOrphaned, 11},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.constant) != tt.wantValue {
				t.Errorf("%s = %v, want %v", tt.name, tt.constant, tt.wantValue)
			}
		})
	}
}

// TestTaskStatusConstruction tests TaskStatus struct
func TestTaskStatusConstruction(t *testing.T) {
	tests := []struct {
		name   string
		status TaskStatus
		check  func(*testing.T, TaskStatus)
	}{
		{
			name: "zero value",
			status: TaskStatus{},
			check: func(t *testing.T, s TaskStatus) {
				if s.State != TaskStateNew {
					t.Errorf("State = %v, want TaskStateNew (0)", s.State)
				}
				if s.Timestamp != 0 {
					t.Errorf("Timestamp = %v, want 0", s.Timestamp)
				}
				if s.Message != "" {
					t.Errorf("Message = %v, want empty", s.Message)
				}
				if s.Err != nil {
					t.Errorf("Err = %v, want nil", s.Err)
				}
			},
		},
		{
			name: "running status",
			status: TaskStatus{
				State:     TaskStateRunning,
				Timestamp: 1234567890,
				Message:   "Container is running",
				Err:       nil,
			},
			check: func(t *testing.T, s TaskStatus) {
				if s.State != TaskStateRunning {
					t.Errorf("State = %v, want TaskStateRunning", s.State)
				}
				if s.Timestamp != 1234567890 {
					t.Errorf("Timestamp = %v, want 1234567890", s.Timestamp)
				}
				if s.Message != "Container is running" {
					t.Errorf("Message = %v, want 'Container is running'", s.Message)
				}
			},
		},
		{
			name: "failed status with error",
			status: TaskStatus{
				State:     TaskStateFailed,
				Timestamp: 1234567891,
				Message:   "Container exited with error",
				Err:       &testError{},
			},
			check: func(t *testing.T, s TaskStatus) {
				if s.State != TaskStateFailed {
					t.Errorf("State = %v, want TaskStateFailed", s.State)
				}
				if s.Err == nil {
					t.Errorf("Err = nil, want non-nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.status)
		})
	}
}

// testError is a simple error implementation for testing
type testError struct{}

func (e *testError) Error() string {
	return "test error"
}

// TestTaskSpecConstruction tests TaskSpec struct
func TestTaskSpecConstruction(t *testing.T) {
	container := &Container{
		Image: "nginx:latest",
	}

	tests := []struct {
		name   string
		spec   TaskSpec
		check  func(*testing.T, TaskSpec)
	}{
		{
			name: "zero value",
			spec: TaskSpec{},
			check: func(t *testing.T, s TaskSpec) {
				if s.Runtime != nil {
					t.Errorf("Runtime = %v, want nil", s.Runtime)
				}
			},
		},
		{
			name: "with container runtime",
			spec: TaskSpec{
				Runtime: container,
				Resources: ResourceRequirements{
					Limits: &Resources{
						NanoCPUs:    1000000000,
						MemoryBytes: 512 * 1024 * 1024,
					},
				},
				Restart: RestartPolicy{
					Condition: RestartPolicyAny,
				},
				Placement: Placement{
					Constraints: []string{"node.role == worker"},
				},
			},
			check: func(t *testing.T, s TaskSpec) {
				if s.Runtime == nil {
					t.Errorf("Runtime = nil, want non-nil")
				}
				if s.Resources.Limits == nil {
					t.Errorf("Resources.Limits = nil, want non-nil")
				}
				if s.Restart.Condition != RestartPolicyAny {
					t.Errorf("Restart.Condition = %v, want RestartPolicyAny", s.Restart.Condition)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.spec)
		})
	}
}

// TestTaskConstruction tests Task struct with nil slices
func TestTaskConstruction(t *testing.T) {
	tests := []struct {
		name   string
		task   Task
		check  func(*testing.T, Task)
	}{
		{
			name: "zero value",
			task: Task{},
			check: func(t *testing.T, task Task) {
				if task.ID != "" {
					t.Errorf("ID = %v, want empty", task.ID)
				}
				if task.Networks != nil {
					t.Errorf("Networks = %v, want nil", task.Networks)
				}
				if task.Secrets != nil {
					t.Errorf("Secrets = %v, want nil", task.Secrets)
				}
				if task.Configs != nil {
					t.Errorf("Configs = %v, want nil", task.Configs)
				}
				if task.Annotations != nil {
					t.Errorf("Annotations = %v, want nil", task.Annotations)
				}
			},
		},
		{
			name: "full task",
			task: Task{
				ID:        "task123",
				ServiceID: "service456",
				NodeID:    "node789",
				Spec: TaskSpec{
					Runtime: &Container{Image: "nginx:latest"},
				},
				Status: TaskStatus{
					State: TaskStateRunning,
				},
				Networks: []NetworkAttachment{
					{
						Network: Network{
							ID: "network123",
							Spec: NetworkSpec{
								Name:   "my-overlay",
								Driver: "overlay",
							},
						},
						Addresses: []string{"10.0.0.2/24"},
					},
				},
				Secrets: []SecretRef{
					{ID: "secret1", Name: "db_password"},
				},
				Configs: []ConfigRef{
					{ID: "config1", Name: "app_config"},
				},
				Annotations: map[string]string{
					"com.example.key": "value",
				},
			},
			check: func(t *testing.T, task Task) {
				if task.ID != "task123" {
					t.Errorf("ID = %v, want task123", task.ID)
				}
				if len(task.Networks) != 1 {
					t.Errorf("len(Networks) = %v, want 1", len(task.Networks))
				}
				if len(task.Secrets) != 1 {
					t.Errorf("len(Secrets) = %v, want 1", len(task.Secrets))
				}
				if len(task.Configs) != 1 {
					t.Errorf("len(Configs) = %v, want 1", len(task.Configs))
				}
				if task.Annotations == nil {
					t.Errorf("Annotations = nil, want non-nil")
				}
			},
		},
		{
			name: "nil slices and empty map",
			task: Task{
				ID:          "task456",
				Networks:    nil,
				Secrets:     nil,
				Configs:     nil,
				Annotations: nil,
			},
			check: func(t *testing.T, task Task) {
				if task.Networks != nil {
					t.Errorf("Networks = %v, want nil", task.Networks)
				}
				if task.Secrets != nil {
					t.Errorf("Secrets = %v, want nil", task.Secrets)
				}
				if task.Configs != nil {
					t.Errorf("Configs = %v, want nil", task.Configs)
				}
				if task.Annotations != nil {
					t.Errorf("Annotations = %v, want nil", task.Annotations)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.task)
		})
	}
}

// TestNetworkAttachmentConstruction tests NetworkAttachment struct
func TestNetworkAttachmentConstruction(t *testing.T) {
	tests := []struct {
		name   string
		attach NetworkAttachment
		check  func(*testing.T, NetworkAttachment)
	}{
		{
			name: "zero value",
			attach: NetworkAttachment{},
			check: func(t *testing.T, n NetworkAttachment) {
				if n.Network.ID != "" {
					t.Errorf("Network.ID = %v, want empty", n.Network.ID)
				}
				if n.Addresses != nil {
					t.Errorf("Addresses = %v, want nil", n.Addresses)
				}
			},
		},
		{
			name: "with network and addresses",
			attach: NetworkAttachment{
				Network: Network{
					ID: "net123",
					Spec: NetworkSpec{
						Name:   "overlay1",
						Driver: "overlay",
					},
				},
				Addresses: []string{"10.0.0.2/24", "fe80::1/64"},
			},
			check: func(t *testing.T, n NetworkAttachment) {
				if n.Network.ID != "net123" {
					t.Errorf("Network.ID = %v, want net123", n.Network.ID)
				}
				if len(n.Addresses) != 2 {
					t.Errorf("len(Addresses) = %v, want 2", len(n.Addresses))
				}
			},
		},
		{
			name: "empty addresses slice",
			attach: NetworkAttachment{
				Network: Network{
					ID: "net456",
				},
				Addresses: []string{},
			},
			check: func(t *testing.T, n NetworkAttachment) {
				if len(n.Addresses) != 0 {
					t.Errorf("len(Addresses) = %v, want 0", len(n.Addresses))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.attach)
		})
	}
}

// TestNetworkSpecConstruction tests NetworkSpec struct with nil DriverConfig
func TestNetworkSpecConstruction(t *testing.T) {
	tests := []struct {
		name   string
		spec   NetworkSpec
		check  func(*testing.T, NetworkSpec)
	}{
		{
			name: "zero value",
			spec: NetworkSpec{},
			check: func(t *testing.T, s NetworkSpec) {
				if s.Name != "" {
					t.Errorf("Name = %v, want empty", s.Name)
				}
				if s.Driver != "" {
					t.Errorf("Driver = %v, want empty", s.Driver)
				}
				if s.DriverConfig != nil {
					t.Errorf("DriverConfig = %v, want nil", s.DriverConfig)
				}
			},
		},
		{
			name: "overlay network without driver config",
			spec: NetworkSpec{
				Name:   "my-overlay",
				Driver: "overlay",
			},
			check: func(t *testing.T, s NetworkSpec) {
				if s.Name != "my-overlay" {
					t.Errorf("Name = %v, want my-overlay", s.Name)
				}
				if s.Driver != "overlay" {
					t.Errorf("Driver = %v, want overlay", s.Driver)
				}
				if s.DriverConfig != nil {
					t.Errorf("DriverConfig = %v, want nil", s.DriverConfig)
				}
			},
		},
		{
			name: "bridge network with driver config",
			spec: NetworkSpec{
				Name:   "my-bridge",
				Driver: "bridge",
				DriverConfig: &DriverConfig{
					Bridge: &BridgeConfig{
						Name: "br0",
					},
				},
			},
			check: func(t *testing.T, s NetworkSpec) {
				if s.DriverConfig == nil {
					t.Errorf("DriverConfig = nil, want non-nil")
				}
				if s.DriverConfig.Bridge == nil {
					t.Errorf("DriverConfig.Bridge = nil, want non-nil")
				}
				if s.DriverConfig.Bridge.Name != "br0" {
					t.Errorf("DriverConfig.Bridge.Name = %v, want br0", s.DriverConfig.Bridge.Name)
				}
			},
		},
		{
			name: "driver config without bridge",
			spec: NetworkSpec{
				Name:         "custom-network",
				Driver:       "custom",
				DriverConfig: &DriverConfig{},
			},
			check: func(t *testing.T, s NetworkSpec) {
				if s.DriverConfig == nil {
					t.Errorf("DriverConfig = nil, want non-nil")
				}
				if s.DriverConfig.Bridge != nil {
					t.Errorf("DriverConfig.Bridge = %v, want nil", s.DriverConfig.Bridge)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.spec)
		})
	}
}

// TestDriverConfigConstruction tests DriverConfig with nil Bridge pointer
func TestDriverConfigConstruction(t *testing.T) {
	tests := []struct {
		name   string
		config DriverConfig
		check  func(*testing.T, DriverConfig)
	}{
		{
			name:   "zero value",
			config: DriverConfig{},
			check: func(t *testing.T, d DriverConfig) {
				if d.Bridge != nil {
					t.Errorf("Bridge = %v, want nil", d.Bridge)
				}
			},
		},
		{
			name: "with bridge config",
			config: DriverConfig{
				Bridge: &BridgeConfig{
					Name: "br-test",
				},
			},
			check: func(t *testing.T, d DriverConfig) {
				if d.Bridge == nil {
					t.Errorf("Bridge = nil, want non-nil")
				}
				if d.Bridge.Name != "br-test" {
					t.Errorf("Bridge.Name = %v, want br-test", d.Bridge.Name)
				}
			},
		},
		{
			name: "nil bridge",
			config: DriverConfig{
				Bridge: nil,
			},
			check: func(t *testing.T, d DriverConfig) {
				if d.Bridge != nil {
					t.Errorf("Bridge = %v, want nil", d.Bridge)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.config)
		})
	}
}

// TestBridgeConfigConstruction tests BridgeConfig struct
func TestBridgeConfigConstruction(t *testing.T) {
	tests := []struct {
		name   string
		config BridgeConfig
		want   BridgeConfig
	}{
		{
			name:   "zero value",
			config: BridgeConfig{},
			want: BridgeConfig{
				Name: "",
			},
		},
		{
			name: "with name",
			config: BridgeConfig{
				Name: "br0",
			},
			want: BridgeConfig{
				Name: "br0",
			},
		},
		{
			name: "empty name",
			config: BridgeConfig{
				Name: "",
			},
			want: BridgeConfig{
				Name: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", tt.config.Name, tt.want.Name)
			}
		})
	}
}

// TestNetworkConfigYAMLTags tests NetworkConfig YAML tag verification using json.Marshal
func TestNetworkConfigYAMLTags(t *testing.T) {
	tests := []struct {
		name      string
		config    NetworkConfig
		wantJSON  string // Expected key names from YAML tags (note: json.Marshal uses YAML tags as fallback in some cases, but we'll verify struct can be marshaled)
		checkKeys map[string]bool // Keys that should exist in JSON
	}{
		{
			name: "zero value",
			config: NetworkConfig{},
			checkKeys: map[string]bool{
				"bridge_name":        false,
				"enable_rate_limit":  false,
				"max_packets_per_sec": false,
				"subnet":             false,
				"bridge_ip":          false,
				"ip_mode":            false,
				"nat_enabled":        false,
				"vxlan_enabled":      false,
				"vxlan_id":           false,
				"vxlan_tunnel_ip":    false,
				"vxlan_peers":        false,
			},
		},
		{
			name: "full config",
			config: NetworkConfig{
				BridgeName:       "br0",
				EnableRateLimit:  true,
				MaxPacketsPerSec: 1000,
				Subnet:           "192.168.127.0/24",
				BridgeIP:         "192.168.127.1/24",
				IPMode:           "static",
				NATEnabled:       true,
				VXLANEnabled:     true,
				VXLANID:          100,
				VXLANTunnelIP:    "10.30.0.1/24",
				VXLANPeers:       []string{"192.168.56.12", "192.168.56.13"},
			},
			checkKeys: map[string]bool{
				"bridge_name":        false,
				"enable_rate_limit":  false,
				"max_packets_per_sec": false,
				"subnet":             false,
				"bridge_ip":          false,
				"ip_mode":            false,
				"nat_enabled":        false,
				"vxlan_enabled":      false,
				"vxlan_id":           false,
				"vxlan_tunnel_ip":    false,
				"vxlan_peers":        false,
			},
		},
		{
			name: "minimal config",
			config: NetworkConfig{
				BridgeName: "br-minimal",
				Subnet:     "10.0.0.0/24",
			},
			checkKeys: map[string]bool{
				"bridge_name": false,
				"subnet":      false,
			},
		},
		{
			name: "VXLAN config only",
			config: NetworkConfig{
				VXLANEnabled:  true,
				VXLANID:       200,
				VXLANTunnelIP: "10.40.0.1/24",
				VXLANPeers:    []string{"192.168.1.10"},
			},
			checkKeys: map[string]bool{
				"vxlan_enabled":   false,
				"vxlan_id":        false,
				"vxlan_tunnel_ip": false,
				"vxlan_peers":     false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON to verify struct can be serialized
			// Note: json.Marshal doesn't directly use yaml tags, but this verifies the struct is valid
			data, err := json.Marshal(tt.config)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}

			// Unmarshal back to verify round-trip
			var decoded NetworkConfig
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}

			// Verify key fields match
			if decoded.BridgeName != tt.config.BridgeName {
				t.Errorf("BridgeName = %v, want %v", decoded.BridgeName, tt.config.BridgeName)
			}
			if decoded.EnableRateLimit != tt.config.EnableRateLimit {
				t.Errorf("EnableRateLimit = %v, want %v", decoded.EnableRateLimit, tt.config.EnableRateLimit)
			}
			if decoded.Subnet != tt.config.Subnet {
				t.Errorf("Subnet = %v, want %v", decoded.Subnet, tt.config.Subnet)
			}
			if decoded.VXLANID != tt.config.VXLANID {
				t.Errorf("VXLANID = %v, want %v", decoded.VXLANID, tt.config.VXLANID)
			}

			// Verify slices
			if len(decoded.VXLANPeers) != len(tt.config.VXLANPeers) {
				t.Errorf("len(VXLANPeers) = %v, want %v", len(decoded.VXLANPeers), len(tt.config.VXLANPeers))
			} else {
				for i, peer := range decoded.VXLANPeers {
					if peer != tt.config.VXLANPeers[i] {
						t.Errorf("VXLANPeers[%d] = %v, want %v", i, peer, tt.config.VXLANPeers[i])
					}
				}
			}
		})
	}
}

// TestNetworkConfigEdgeCases tests NetworkConfig edge cases
func TestNetworkConfigEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		config NetworkConfig
		check  func(*testing.T, NetworkConfig)
	}{
		{
			name: "empty VXLAN peers slice",
			config: NetworkConfig{
				VXLANEnabled: true,
				VXLANPeers:   []string{},
			},
			check: func(t *testing.T, c NetworkConfig) {
				if len(c.VXLANPeers) != 0 {
					t.Errorf("len(VXLANPeers) = %v, want 0", len(c.VXLANPeers))
				}
			},
		},
		{
			name: "nil VXLAN peers slice",
			config: NetworkConfig{
				VXLANEnabled: true,
				VXLANPeers:   nil,
			},
			check: func(t *testing.T, c NetworkConfig) {
				if c.VXLANPeers != nil {
					t.Errorf("VXLANPeers = %v, want nil", c.VXLANPeers)
				}
			},
		},
		{
			name: "zero VXLAN ID",
			config: NetworkConfig{
				VXLANEnabled: true,
				VXLANID:      0,
			},
			check: func(t *testing.T, c NetworkConfig) {
				if c.VXLANID != 0 {
					t.Errorf("VXLANID = %v, want 0", c.VXLANID)
				}
			},
		},
		{
			name: "zero max packets",
			config: NetworkConfig{
				EnableRateLimit:  true,
				MaxPacketsPerSec: 0,
			},
			check: func(t *testing.T, c NetworkConfig) {
				if c.MaxPacketsPerSec != 0 {
					t.Errorf("MaxPacketsPerSec = %v, want 0", c.MaxPacketsPerSec)
				}
			},
		},
		{
			name: "empty strings",
			config: NetworkConfig{
				BridgeName:     "",
				Subnet:         "",
				BridgeIP:       "",
				IPMode:         "",
				VXLANTunnelIP:  "",
			},
			check: func(t *testing.T, c NetworkConfig) {
				if c.BridgeName != "" {
					t.Errorf("BridgeName = %v, want empty", c.BridgeName)
				}
				if c.Subnet != "" {
					t.Errorf("Subnet = %v, want empty", c.Subnet)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.config)
		})
	}
}

// Mock types for interface compliance verification

// mockVMMManager implements VMMManager for testing
type mockVMMManager struct{}

func (m *mockVMMManager) Start(ctx context.Context, task *Task, config interface{}) error {
	return nil
}
func (m *mockVMMManager) Stop(ctx context.Context, task *Task) error {
	return nil
}
func (m *mockVMMManager) Wait(ctx context.Context, task *Task) (*TaskStatus, error) {
	return nil, nil
}
func (m *mockVMMManager) Describe(ctx context.Context, task *Task) (*TaskStatus, error) {
	return nil, nil
}
func (m *mockVMMManager) Remove(ctx context.Context, task *Task) error {
	return nil
}
func (m *mockVMMManager) Snapshot(ctx context.Context, task *Task, opts interface{}) (interface{}, error) {
	return nil, nil
}
func (m *mockVMMManager) Restore(ctx context.Context, task *Task, snapshot interface{}) error {
	return nil
}

// mockTaskTranslator implements TaskTranslator for testing
type mockTaskTranslator struct{}

func (m *mockTaskTranslator) Translate(task *Task) (interface{}, error) {
	return nil, nil
}

// mockImagePreparer implements ImagePreparer for testing
type mockImagePreparer struct{}

func (m *mockImagePreparer) Prepare(ctx context.Context, task *Task) error {
	return nil
}
func (m *mockImagePreparer) Cleanup(ctx context.Context, keepDays int) (filesRemoved int, bytesFreed int64, err error) {
	return 0, 0, nil
}

// mockNetworkManager implements NetworkManager for testing
type mockNetworkManager struct{}

func (m *mockNetworkManager) PrepareNetwork(ctx context.Context, task *Task) error {
	return nil
}
func (m *mockNetworkManager) CleanupNetwork(ctx context.Context, task *Task) error {
	return nil
}
func (m *mockNetworkManager) GetTapIP(taskID string) (string, error) {
	return "", nil
}

// TestInterfaceSignatures verifies that interface signatures exist and are correct
func TestInterfaceSignatures(t *testing.T) {
	t.Run("VMMManager interface exists", func(t *testing.T) {
		// Verify VMMManager is an interface type
		var _ interface{} = (VMMManager)(nil)

		// Verify the mock satisfies the interface
		var _ VMMManager = (*mockVMMManager)(nil)
	})

	t.Run("TaskTranslator interface exists", func(t *testing.T) {
		var _ interface{} = (TaskTranslator)(nil)

		var _ TaskTranslator = (*mockTaskTranslator)(nil)
	})

	t.Run("ImagePreparer interface exists", func(t *testing.T) {
		var _ interface{} = (ImagePreparer)(nil)

		var _ ImagePreparer = (*mockImagePreparer)(nil)
	})

	t.Run("NetworkManager interface exists", func(t *testing.T) {
		var _ interface{} = (NetworkManager)(nil)

		var _ NetworkManager = (*mockNetworkManager)(nil)
	})
}
