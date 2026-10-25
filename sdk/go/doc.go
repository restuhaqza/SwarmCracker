// Package sdk documents the Go SDK for the SwarmCracker API v1.
//
// # The SDK is pkg/client
//
// There is no second transport and no wrapper module: import the module's own
// client package.
//
//	import "github.com/restuhaqza/swarmcracker/pkg/client"
//
// The same client dials the manager's local Unix control socket and the remote
// HTTPS API. A non-empty Endpoint selects the remote transport; without one the
// local control socket is used.
//
//	cfg := client.Config{
//		Endpoint:  "https://mgr:4244",
//		CAFile:    "/etc/swarmcracker/api-ca.pem",
//		TokenFile: "/etc/swarmcracker/api-token",
//	}
//	c, err := client.New(cfg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer c.Close()
//
// CAFile is required whenever Endpoint is set: there is no unverified fallback,
// and plain http is refused. TokenFile is optional (the manager's local socket
// carries no bearer token) and its content is trimmed. Config.Token overrides
// TokenFile. Config.Timeout bounds one request; zero selects the package
// default.
//
// # Methods
//
// Reads:
//
//	ListServices, GetService, ListTasks, GetTask, ListServiceTasks
//	ListNodes, GetNode, ListNetworks, GetNetwork
//	ListConfigs, GetConfig, ListSecrets, GetSecret
//	ClusterID
//
// Writes:
//
//	CreateService, UpdateService (RFC 7386 merge patch), RollbackService, DeleteService
//	CreateConfig, UpdateConfig, DeleteConfig
//	CreateSecret, UpdateSecret, DeleteSecret
//	CreateNetwork, DeleteNetwork, UpdateNode
//
// Streams and execution:
//
//	ExecTask streams one command's stdout and stderr and returns its exit status
//	WatchEvents streams events as a channel of v1.Event plus a channel of errors
//
// Every method takes a context.Context; cancelling it cancels the request or
// the stream. Over the remote HTTPS transport every failure is *output.Error,
// which carries the stable v1 error code (output.CodeInvalid,
// output.CodeNotFound, output.CodeUnreachable, ...). A read on the local Unix
// socket can surface the manager's gRPC status instead, so use errors.As and
// fall back to the status code when the assertion fails.
//
// # Models
//
// The documents this package returns are the DTOs in pkg/api/v1. They are the
// same documents the CLI prints for `-o json`, so a Go program and a shell script
// see one shape. The generated Python and TypeScript models come from the same
// normative spec (api/openapi.yaml), so all three SDKs agree field for field.
//
// See example_test.go in this directory for a compiling example and README.md
// for the surface map.
package sdk
