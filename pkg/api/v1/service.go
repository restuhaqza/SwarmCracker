package v1

// Service is a resource object inside a ServiceList.
type Service struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	Revision  string            `json:"revision"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
	Spec      ServiceSpec       `json:"spec"`
	Status    ServiceStatus     `json:"status"`
}

// ServiceDocument is a single-resource Service document.
type ServiceDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Service
}

// NewServiceDocument wraps a Service in the v1 envelope.
func NewServiceDocument(svc Service) ServiceDocument {
	return ServiceDocument{SchemaVersion: SchemaVersion, Kind: KindService, Service: svc}
}

// ServiceList is a paginated list of services.
type ServiceList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Service `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewServiceList returns a list document with a non-nil items array.
func NewServiceList(items []Service) ServiceList {
	if items == nil {
		items = []Service{}
	}
	return ServiceList{
		SchemaVersion: SchemaVersion,
		Kind:          KindServiceList,
		Items:         items,
		Next:          nil,
	}
}

// ServiceSpec is the writable part of a service.
type ServiceSpec struct {
	Mode      string        `json:"mode"`
	Replicas  *int64        `json:"replicas,omitempty"`
	Task      TaskSpec      `json:"task"`
	Update    *UpdateConfig `json:"update,omitempty"`
	Rollback  *UpdateConfig `json:"rollback,omitempty"`
	Placement *Placement    `json:"placement,omitempty"`
}

// TaskSpec is the task template of a service.
type TaskSpec struct {
	Image          string              `json:"image"`
	Command        []string            `json:"command,omitempty"`
	Args           []string            `json:"args,omitempty"`
	Env            map[string]string   `json:"env,omitempty"`
	User           string              `json:"user,omitempty"`
	Workdir        string              `json:"workdir,omitempty"`
	VCPUs          int64               `json:"vcpus,omitempty"`
	MemoryBytes    int64               `json:"memoryBytes,omitempty"`
	Ports          []Port              `json:"ports,omitempty"`
	Mounts         []Mount             `json:"mounts,omitempty"`
	Networks       []NetworkAttachment `json:"networks,omitempty"`
	RestartPolicy  *RestartPolicy      `json:"restartPolicy,omitempty"`
	Healthcheck    *Healthcheck        `json:"healthcheck,omitempty"`
	StopSignal     string              `json:"stopSignal,omitempty"`
	StopGraceMs    int64               `json:"stopGraceMs,omitempty"`
	ReadOnlyRootfs bool                `json:"readOnlyRootfs,omitempty"`
}

// Port is a published or target port.
type Port struct {
	Target    uint32 `json:"target"`
	Published uint32 `json:"published,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// Mount is a container mount.
type Mount struct {
	Type     string `json:"type"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// NetworkAttachment is a network a task joins.
type NetworkAttachment struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// RestartPolicy is a task's restart policy.
type RestartPolicy struct {
	Condition   string `json:"condition,omitempty"`
	DelayMs     int64  `json:"delayMs,omitempty"`
	MaxAttempts uint64 `json:"maxAttempts,omitempty"`
	WindowMs    int64  `json:"windowMs,omitempty"`
}

// Healthcheck is a container healthcheck.
type Healthcheck struct {
	Test          []string `json:"test"`
	IntervalMs    int64    `json:"intervalMs,omitempty"`
	TimeoutMs     int64    `json:"timeoutMs,omitempty"`
	Retries       int64    `json:"retries,omitempty"`
	StartPeriodMs int64    `json:"startPeriodMs,omitempty"`
}

// UpdateConfig controls a rolling update or rollback.
type UpdateConfig struct {
	Parallelism     uint64  `json:"parallelism,omitempty"`
	DelayMs         int64   `json:"delayMs,omitempty"`
	FailureAction   string  `json:"failureAction,omitempty"`
	Order           string  `json:"order,omitempty"`
	MonitorMs       int64   `json:"monitorMs,omitempty"`
	MaxFailureRatio float32 `json:"maxFailureRatio,omitempty"`
}

// Placement constrains where tasks may run.
type Placement struct {
	Constraints []string `json:"constraints,omitempty"`
}

// ServiceStatus is the observed part of a service.
type ServiceStatus struct {
	DesiredTasks      int       `json:"desiredTasks"`
	RunningTasks      int       `json:"runningTasks"`
	TasksInTransition int       `json:"tasksInTransition"`
	Converged         bool      `json:"converged"`
	Message           string    `json:"message,omitempty"`
	Endpoint          *Endpoint `json:"endpoint,omitempty"`
}

// Endpoint is the resolved service endpoint.
type Endpoint struct {
	Ports []Port `json:"ports,omitempty"`
}
