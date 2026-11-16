package v1

// This file owns the two sandbox documents that carry what a guest says about itself:
// the file-transfer response, and structured status.
//
// Both exist because the alternative is parsing a transcript. A caller that wants to know
// what a sandbox's workspace holds, or what its kernel and network are, asks for the fact
// rather than running a command whose output it then has to read: `stat`, `list`, `tar`,
// `untar`, `info` and `net` are the guest agent's own operations (pkg/agent), and these
// documents are their answers in the v1 envelope.
//
// A file read is the one response in this API that is not a JSON document: the bytes are
// the point, so a GET of a file is application/octet-stream and a GET of a directory is
// application/x-tar. What a write reports — how many bytes, how many entries, what an
// archive could not carry — is a document, because a caller acts on those numbers.

// Kinds for the sandbox file and status documents, beside the sandbox ones.
const (
	// KindSandboxFileWrite is the answer to a write into a sandbox: what the guest
	// wrote, not what the caller sent.
	KindSandboxFileWrite = "SandboxFileWrite"
	// KindSandboxStatus is one sandbox plus the two reports its guest made about
	// itself (its identity and resources, and its network).
	KindSandboxStatus = "SandboxStatus"
)

// SandboxFileWrite is the response to PUT /v1/sandboxes/{id}/files.
type SandboxFileWrite struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	// Path is the guest path the bytes were written to, exactly as the caller named
	// it. A guest path is the guest's own namespace (the agent confines every file
	// operation to its workspace root), so it is echoed rather than resolved.
	Path string `json:"path"`
	// Bytes is how many file bytes the guest wrote: the copied file's size for a
	// file, and the sum of the files in an archive for a tree — what landed, not
	// what was sent.
	Bytes int64 `json:"bytes"`
	// Entries is how many entries an archive wrote, and is zero for a single file.
	// A tree is more than the bytes in it — a directory, a mode, a link — so a
	// caller that copies a tree reads this to know how much of it arrived.
	Entries int `json:"entries,omitempty"`
	// Warnings names archive entries the guest did not write, and why. A write that
	// skips an entry a workspace copy cannot hold (a device, a fifo) says so here
	// rather than reporting success over a tree that is missing something.
	Warnings []string `json:"warnings,omitempty"`
}

// NewSandboxFileWrite builds the write's answer.
func NewSandboxFileWrite(path string, written int64, entries int, warnings []string) SandboxFileWrite {
	return SandboxFileWrite{
		SchemaVersion: SchemaVersion,
		Kind:          KindSandboxFileWrite,
		Path:          path,
		Bytes:         written,
		Entries:       entries,
		Warnings:      warnings,
	}
}

// SandboxGuestInfo is what the guest knows about itself, read from its own /proc and
// /etc/os-release rather than inferred from the image (pkg/agent's info.go).
type SandboxGuestInfo struct {
	// Hostname and MachineID are the guest's identity. MachineID is what the
	// post-restore hook rewrites, so two clones of one checkpoint differ here.
	Hostname  string `json:"hostname,omitempty"`
	MachineID string `json:"machineId,omitempty"`
	// KernelRelease and KernelVersion are the kernel the guest is actually running,
	// which is the guest's and not the host's.
	KernelRelease string `json:"kernelRelease,omitempty"`
	KernelVersion string `json:"kernelVersion,omitempty"`
	// Machine is the guest's architecture as its kernel names it.
	Machine string `json:"machine,omitempty"`
	// OSName and OSID come from the template's /etc/os-release. A template without
	// one reports both empty rather than a guess.
	OSName string `json:"osName,omitempty"`
	OSID   string `json:"osId,omitempty"`
	// CPUs and MemoryTotalBytes are the resources the guest believes it has.
	CPUs             int    `json:"cpus,omitempty"`
	MemoryTotalBytes uint64 `json:"memoryTotalBytes,omitempty"`
	// UptimeSeconds is the guest's own uptime. A restore rewrites it, so it measures
	// the running guest and not the checkpoint it came from.
	UptimeSeconds float64 `json:"uptimeSeconds,omitempty"`
	// Protocol, AgentVersion, BuildID and BootID describe the agent that answered.
	Protocol     string `json:"protocol,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
	BuildID      string `json:"buildId,omitempty"`
	BootID       string `json:"bootId,omitempty"`
	// Tools are the names of the tools the template's own directories hold, sorted by
	// name. It is how a caller tells whether a template ships a shell or tar.
	Tools []string `json:"tools,omitempty"`
}

// SandboxNetInterface is one of the guest's links.
type SandboxNetInterface struct {
	Name string `json:"name"`
	// Index is the kernel's interface index, which is what a route's device column
	// refers to.
	Index int    `json:"index"`
	MAC   string `json:"mac,omitempty"`
	MTU   int    `json:"mtu,omitempty"`
	Up    bool   `json:"up"`
	// Loopback marks lo: a sandbox's loopback is its own.
	Loopback bool `json:"loopback,omitempty"`
	// Addresses are the interface's addresses in CIDR form, sorted.
	Addresses []string `json:"addresses,omitempty"`
}

// SandboxNetRoute is one route the guest installed.
type SandboxNetRoute struct {
	// Family is "ipv4" or "ipv6".
	Family      string `json:"family"`
	Destination string `json:"destination"`
	// Gateway is the next hop, and is absent for an on-link route.
	Gateway string `json:"gateway,omitempty"`
	Device  string `json:"device,omitempty"`
	Metric  int    `json:"metric,omitempty"`
}

// SandboxGuestNet is the guest's own view of its network. It is what a caller reads to
// tell "the sandbox cannot reach the registry" from "the sandbox has no route at all",
// which is a question about the guest and not about the node.
type SandboxGuestNet struct {
	Interfaces []SandboxNetInterface `json:"interfaces"`
	Routes     []SandboxNetRoute     `json:"routes"`
	// Resolvers and Search are the guest's /etc/resolv.conf. An empty resolver means
	// the guest has none configured, not that the read failed.
	Resolvers []string `json:"resolvers"`
	Search    []string `json:"search,omitempty"`
}

// SandboxStatus is what one sandbox is: the record the node keeps, and the two documents
// the guest reports about itself.
type SandboxStatus struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	// Sandbox is the same document GET /v1/sandboxes/{id} serves, so a caller reads its
	// state and its own fields from one place in both answers.
	Sandbox
	// Info and Net are the guest's answers. They are read in the same request, so a
	// caller never sees a status assembled from two moments.
	Info SandboxGuestInfo `json:"info"`
	Net  SandboxGuestNet  `json:"net"`
}

// NewSandboxStatus wraps a sandbox and its guest's reports in the v1 envelope.
func NewSandboxStatus(rec Sandbox, info SandboxGuestInfo, net SandboxGuestNet) SandboxStatus {
	if net.Interfaces == nil {
		net.Interfaces = []SandboxNetInterface{}
	}
	if net.Routes == nil {
		net.Routes = []SandboxNetRoute{}
	}
	if net.Resolvers == nil {
		net.Resolvers = []string{}
	}
	return SandboxStatus{
		SchemaVersion: SchemaVersion,
		Kind:          KindSandboxStatus,
		Sandbox:       rec,
		Info:          info,
		Net:           net,
	}
}
