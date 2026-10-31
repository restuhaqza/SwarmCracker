// Package broker is the internal manager-to-node transport for the
// programmatic API. It carries the /v1/tasks/{taskID}/{op} operations that need
// to run on the node that hosts a task (exec, attach, logs, files, health,
// metrics) between a manager and that node.
//
// The broker is not a public surface. It is served over the cluster's mutual
// TLS identity, on port 4245 by default, and a node broker never receives a
// user bearer token: the manager resolves the caller's task reference to a
// concrete task, strips the public credentials and only then proxies.
//
// # Path contract
//
// A node broker serves exactly one route:
//
//	/v1/tasks/{taskID}/{op}
//
// {taskID} is a full task ID, never a name or a prefix; the manager resolves
// user input before proxying. {op} is one of the lowercase operation names a
// later phase registers on the node. An operation whose name carries one
// namespace segment - migration/export, migration/file, migration/stage,
// migration/discard - is spelled as two path segments
// (/v1/tasks/{taskID}/migration/export); any other extra segment is a malformed
// path. A malformed path or an unknown task is a 404; a known task with an
// unregistered operation is a 501.
//
// The migration operations other than export are artifact-scoped: an artifact is
// addressed by its own ID, it outlives the task that produced it, and SwarmKit
// replaces that task as part of the move. The server therefore does not resolve the
// task in their path - file, stage and discard answer for a task this node does not
// have, under any task ID - and a handler is given no task. export keeps the
// task-scoped contract, because it snapshots the task it names.
//
// A migration is also the one case where the manager addresses a node directly
// instead of resolving it from a task (Manager.routeForNode): the target has no
// task yet - SwarmKit creates it when the service is replaced - so there is nothing
// to resolve. The manager initiates every transfer, so the artifact moves source
// node -> manager -> target node and there is no public route for it: a client
// asks for a migration, never for a file of one.
//
// Failures the broker generates itself are v1 Error documents, so a manager
// handler can relay them without translating: not_found (404), not_implemented
// (501) and unavailable (503, a task-source failure or a node the manager could
// not reach). "unreachable" is deliberately absent: the contract reserves it for
// the client, which synthesizes it from a failed dial.
//
// On the way back to a public caller the manager rewrites a relayed 4xx/5xx
// Error document into the public envelope (the v1 media type, the API version
// header and the caller's requestId), so a node's typed 400 for a directory or
// 413 for an oversized upload reaches the caller in the same shape. A successful
// response — the NDJSON exec and log streams and the raw file bytes — is
// forwarded byte for byte.
//
// # Operations
//
// Exec (non-interactive only), attach, logs and files are served. Logs read the
// task's Firecracker console file, so they need no guest agent and remain
// available after the task exits; files travel through the guest agent over the
// vsock muxer; attach relays the task's interactive console Unix socket in both
// directions on one full-duplex HTTP/1.1 request. Exec, files and attach
// therefore need a running task, and a log-only task (the VM is gone, the console
// file is not) answers a typed 404 for them. Interactive TTY over exec is a
// follow-up, so a tty request is a typed 501 with details.reason
// "tty_not_supported"; the console is attach's job instead.
//
// Health and metrics answer from node-local observations rather than from a
// dialed guest: health reads the per-task sidecar the executor publishes, and
// metrics reads the last in-guest sample the daemon's poller cached. Both are
// bounded, best-effort reads a manager fans out per task, so a task with no
// observation yet is a typed 404 with a details.reason that says which answer it
// is, never a fabricated zero.
//
// # Trust
//
// Task IDs arrive as untrusted path input. The default TaskSource,
// SocketDirSource, rejects empty IDs, path separators and dots, so a request
// can never address a file outside the Firecracker socket directory.
package broker
