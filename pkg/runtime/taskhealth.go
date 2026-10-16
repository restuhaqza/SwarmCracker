package runtime

import "sort"

// TaskHealthPath is the daemon endpoint that serves the live health of the
// tasks a node hosts. Both the daemon (which serves it) and the CLI (which
// fans out to it) import this constant so the path cannot drift.
const TaskHealthPath = "/v1/tasks/health"

// TaskHealth is one task's live health in a TaskHealthResponse.
type TaskHealth struct {
	// TaskID identifies the task the health belongs to.
	TaskID string `json:"task_id"`
	// VMHealth is the last observation the daemon published for the task. It is
	// flattened into the JSON object rather than nested.
	VMHealth
}

// TaskHealthResponse is the body of GET TaskHealthPath. Tasks that declare no
// healthcheck never publish one, so they are simply absent; the CLI falls back
// to its state-derived value for those.
type TaskHealthResponse struct {
	Tasks []TaskHealth `json:"tasks"`
}

// CollectTaskHealth returns the live health of every task under dir that has
// published one, sorted by task ID so the response is deterministic. It is the
// whole body of the endpoint: it reads the same per-task sidecars the node-local
// `vm status`/`vm list` use, and never fails — an unreadable or malformed
// sidecar is skipped by ListVMMetadata.
func CollectTaskHealth(dir string) []TaskHealth {
	mds := ListVMMetadata(dir)
	out := make([]TaskHealth, 0, len(mds))
	for _, md := range mds {
		if md.Health == nil {
			continue
		}
		out = append(out, TaskHealth{TaskID: md.ID, VMHealth: *md.Health})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}
