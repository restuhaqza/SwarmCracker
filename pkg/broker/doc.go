// Package broker is the internal manager-to-node transport for the
// programmatic API. It carries the operations that need to run on the node that
// hosts a task (exec, logs, files, health) between a manager and that node.
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
// later phase registers on the node. A malformed path or an unknown task is a
// 404; a known task with an unregistered operation is a 501.
//
// Failures the broker generates itself are v1 Error documents, so a manager
// handler can relay them without translating: not_found (404), not_implemented
// (501), unavailable (503, a task-source failure) and unreachable (503, the
// manager could not reach the node).
//
// On the way back to a public caller the manager rewrites a relayed 404, 501 or
// 503 Error document into the public envelope (the v1 media type, the API
// version header and the caller's requestId). A successful response — including
// the NDJSON exec stream — is forwarded byte for byte.
//
// # Operations
//
// Exec is served today (non-interactive only). Interactive TTY over the API is
// a WebSocket follow-up, so a tty request is a typed 501 with
// details.reason "tty_not_supported".
//
// # Trust
//
// Task IDs arrive as untrusted path input. The default TaskSource,
// SocketDirSource, rejects empty IDs, path separators and dots, so a request
// can never address a file outside the Firecracker socket directory.
package broker
