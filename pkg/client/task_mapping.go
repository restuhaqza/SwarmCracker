package client

import (
	"sort"
	"strconv"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// taskFromSwarmkit maps one SwarmKit task to the v1 read model. nodeNames
// carries the node ID -> hostname lookup built from a single ListNodes call.
func taskFromSwarmkit(task *api.Task, nodeNames map[string]string) v1.Task {
	return v1.Task{
		ID:           task.ID,
		Name:         taskName(task),
		Revision:     strconv.FormatUint(task.Meta.Version.Index, 10),
		ServiceID:    task.ServiceID,
		ServiceName:  task.ServiceAnnotations.Name,
		NodeID:       task.NodeID,
		NodeName:     nodeNames[task.NodeID],
		Slot:         task.Slot,
		State:        strings.ToLower(task.Status.State.String()),
		DesiredState: strings.ToLower(task.DesiredState.String()),
		Message:      task.Status.Message,
		Error:        task.Status.Err,
		CreatedAt:    timestamp(task.Meta.CreatedAt),
		UpdatedAt:    timestamp(task.Meta.UpdatedAt),
		Image:        taskImage(task),
		Addresses:    taskAddresses(task.Networks),
		Ports:        taskPorts(task),
		ExitCode:     taskExitCode(&task.Status),
		// Health is a phase-3 capability; phase 0 never populates it.
	}
}

func taskImage(task *api.Task) string {
	container := task.Spec.GetContainer()
	if container == nil {
		return ""
	}
	return container.Image
}

func taskAddresses(networks []*api.NetworkAttachment) []string {
	var out []string
	for _, network := range networks {
		if network == nil {
			continue
		}
		out = append(out, network.Addresses...)
	}
	return out
}

// taskPorts prefers the ports the agent actually allocated, falling back to
// the task's endpoint ports before the task has run. Published and Mode are
// service-level facts and are never set on a task port.
func taskPorts(task *api.Task) []v1.Port {
	var source []*api.PortConfig
	if task.Endpoint != nil {
		source = task.Endpoint.Ports
	}
	if status := task.Status.PortStatus; status != nil && len(status.Ports) > 0 {
		source = status.Ports
	}
	if len(source) == 0 {
		return nil
	}
	out := make([]v1.Port, 0, len(source))
	for _, port := range source {
		if port == nil {
			continue
		}
		out = append(out, v1.Port{
			Target:   port.TargetPort,
			Protocol: protocolString(port.Protocol),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// taskExitCode is only meaningful once the task's container has terminated.
func taskExitCode(status *api.TaskStatus) *int32 {
	switch status.State {
	case api.TaskStateCompleted, api.TaskStateFailed:
	default:
		return nil
	}
	container := status.GetContainer()
	if container == nil {
		return nil
	}
	code := container.ExitCode
	return &code
}

// taskName follows the contract rule: an explicit annotation name wins;
// otherwise the name is derived from the service, slot and short task ID.
func taskName(task *api.Task) string {
	if task.Annotations.Name != "" {
		return task.Annotations.Name
	}
	serviceName := task.ServiceAnnotations.Name
	if serviceName == "" {
		return ""
	}
	shortID := shortTaskID(task.ID)
	if task.Slot > 0 {
		return serviceName + "." + strconv.FormatUint(task.Slot, 10) + "." + shortID
	}
	return serviceName + "." + shortID
}

const shortTaskIDLength = 12

func shortTaskID(id string) string {
	if len(id) > shortTaskIDLength {
		return id[:shortTaskIDLength]
	}
	return id
}

// taskCreatedAt returns a sortable creation time; a nil timestamp sorts as the
// zero time.
func taskCreatedAt(task *api.Task) time.Time {
	return timestampTime(task.Meta.CreatedAt)
}

func timestampTime(ts *gogotypes.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return time.Unix(ts.Seconds, int64(ts.Nanos))
}

// sortTasks orders by creation time ascending, then ID ascending. It sorts a
// copy and leaves the caller's slice untouched.
func sortTasks(tasks []*api.Task) []*api.Task {
	sorted := make([]*api.Task, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		sorted = append(sorted, task)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := taskCreatedAt(sorted[i]), taskCreatedAt(sorted[j])
		if !left.Equal(right) {
			return left.Before(right)
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// nodeNameIndex builds the node ID -> hostname lookup from one ListNodes
// response. Nodes without a description are skipped because they cannot
// contribute a hostname.
func nodeNameIndex(nodes []*api.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, node := range nodes {
		if node == nil || node.Description == nil {
			continue
		}
		out[node.ID] = node.Description.Hostname
	}
	return out
}
