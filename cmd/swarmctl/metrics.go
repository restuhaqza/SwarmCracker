package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/redact"
)

// getTaskMetrics retrieves resource metrics from a running Firecracker VM.
func getTaskMetrics(ctx context.Context, client api.ControlClient, taskID string) {
	socketDir := "/var/run/firecracker"
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "VM socket not found for task %s\n", taskID)
		os.Exit(1)
	}

	// Query Firecracker API for machine config
	clientHTTP := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	// Get machine config
	resp, err := clientHTTP.Get("http://localhost/machine-config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get machine config: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read response: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Machine Configuration ===")
	fmt.Println(string(body))

	// Get instance info
	resp2, err := clientHTTP.Get("http://localhost/")
	if err == nil {
		defer resp2.Body.Close()
		body2, _ := io.ReadAll(resp2.Body)
		fmt.Println("=== Instance Info ===")
		fmt.Println(string(body2))
	}
}

// marshalJSON renders v as the indented JSON swarmctl prints. Service and task
// responses carry a registry credential on their container spec's PullOptions,
// so they are redacted first (pkg/redact, the same implementation the
// swarmcracker CLI uses): a credential must never reach stdout.
//
// This is the choke point for every JSON document the binary prints, so the
// switch covers each shape a credential can arrive in — a single response and a
// list of them — and refuses anything else that could still hold a container
// spec, rather than printing it unredacted.
func marshalJSON(v interface{}) ([]byte, error) {
	switch obj := v.(type) {
	case *api.Service:
		v = redact.Service(obj)
	case *api.Task:
		v = redact.Task(obj)
	case []*api.Service:
		redacted := make([]*api.Service, len(obj))
		for i, svc := range obj {
			redacted[i] = redact.Service(svc)
		}
		v = redacted
	case []*api.Task:
		redacted := make([]*api.Task, len(obj))
		for i, task := range obj {
			redacted[i] = redact.Task(task)
		}
		v = redacted
	default:
		if couldCarryContainerSpec(reflect.TypeOf(v)) {
			return nil, fmt.Errorf("refusing to print %T: it can carry a registry credential and no redaction applies", v)
		}
	}
	return json.MarshalIndent(v, "", "  ")
}

// couldCarryContainerSpec reports whether a type can transitively hold an
// api.ContainerSpec (or a Task/Service spec, which embeds one) — the struct that
// carries the X-Registry-Auth payload. It inspects the type rather than the
// value, so it is stable across requests, and it is only reached for shapes the
// type switch above does not already redact. Interface fields are treated as
// opaque: a concrete type reached that way cannot be identified without a
// value, and the known spec types are matched directly.
func couldCarryContainerSpec(t reflect.Type) bool {
	return typeCarriesContainerSpec(t, make(map[reflect.Type]bool))
}

func typeCarriesContainerSpec(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return typeCarriesContainerSpec(t.Elem(), seen)
	case reflect.Struct:
		switch t {
		case reflect.TypeOf(api.ContainerSpec{}), reflect.TypeOf(api.TaskSpec{}), reflect.TypeOf(api.ServiceSpec{}):
			return true
		}
		for i := 0; i < t.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			if typeCarriesContainerSpec(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// printJSON marshals and prints a value as indented JSON, redacting registry
// credentials on service and task responses.
func printJSON(v interface{}) {
	data, err := marshalJSON(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// parseInt parses a decimal string into a uint64.
func parseInt(s string) (uint64, error) {
	var result uint64
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}
