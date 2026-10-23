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
// later phase registers on the node (exec, logs, files, health). Anything else
// is a 404. An unregistered operation on a known task is a 501.
//
// Failures the broker generates itself are v1 Error documents, so a manager
// handler can relay them without translating: not_found (404), not_implemented
// (501), unavailable (503, a task-source failure) and unreachable (503, the
// manager could not reach the node).
//
// # Trust
//
// Task IDs arrive as untrusted path input. The default TaskSource,
// SocketDirSource, rejects empty IDs, path separators and dots, so a request
// can never address a file outside the Firecracker socket directory.
package broker
