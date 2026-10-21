package v1

// Task is a single task (a scheduled unit of work) inside a TaskList.
//
// The fields are the phase-0 read model: identity, the service and node the
// task belongs to, its observed and desired state, and the runtime facts the
// manager can source locally. Health is defined for contract completeness and is
// never populated in phase 0.
type Task struct {
	ID           string      `json:"id"`
	Name         string      `json:"name,omitempty"`
	Revision     string      `json:"revision"`
	ServiceID    string      `json:"serviceId"`
	ServiceName  string      `json:"serviceName,omitempty"`
	NodeID       string      `json:"nodeId,omitempty"`
	NodeName     string      `json:"nodeName,omitempty"`
	Slot         uint64      `json:"slot,omitempty"`
	State        string      `json:"state"`
	DesiredState string      `json:"desiredState"`
	Message      string      `json:"message,omitempty"`
	Error        string      `json:"error,omitempty"`
	CreatedAt    string      `json:"createdAt,omitempty"`
	UpdatedAt    string      `json:"updatedAt,omitempty"`
	Image        string      `json:"image,omitempty"`
	Addresses    []string    `json:"addresses,omitempty"`
	Ports        []Port      `json:"ports,omitempty"`
	ExitCode     *int32      `json:"exitCode,omitempty"`
	Health       *TaskHealth `json:"health,omitempty"`
}

// TaskHealth is the last healthcheck result reported for a task. It is a
// phase-3 capability; phase 0 leaves Health nil so the key is omitted.
type TaskHealth struct {
	Status        string `json:"status"`
	FailingStreak int    `json:"failingStreak,omitempty"`
	Output        string `json:"output,omitempty"`
	Command       string `json:"command,omitempty"`
	CheckedAt     string `json:"checkedAt,omitempty"`
}

// TaskDocument is a single-resource Task document.
type TaskDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Task
}

// NewTaskDocument wraps a Task in the v1 envelope.
func NewTaskDocument(task Task) TaskDocument {
	return TaskDocument{SchemaVersion: SchemaVersion, Kind: KindTask, Task: task}
}

// TaskList is a paginated list of tasks.
type TaskList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Task    `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewTaskList returns a list document with a non-nil items array.
func NewTaskList(items []Task) TaskList {
	if items == nil {
		items = []Task{}
	}
	return TaskList{
		SchemaVersion: SchemaVersion,
		Kind:          KindTaskList,
		Items:         items,
		Next:          nil,
	}
}
