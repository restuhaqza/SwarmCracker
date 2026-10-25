package sdk_test

import (
	"context"
	"errors"
	"fmt"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// Example is the flow a Go consumer writes against a live manager.
//
// It is compiled by `go test ./sdk/go` but not executed: it has no Output
// comment because it needs a reachable manager, a CA bundle and a token. The
// runnable proof that the same construction reports a typed configuration error
// without a network call is Example_configurationError.
func Example() {
	c, err := client.New(client.Config{
		Endpoint:  "https://mgr:4244",
		CAFile:    "/etc/swarmcracker/api-ca.pem",
		TokenFile: "/etc/swarmcracker/api-token",
	})
	if err != nil {
		fmt.Println("configuration error:", err)
		return
	}
	defer c.Close()

	services, err := c.ListServices(context.Background())
	if err != nil {
		fmt.Println("list services:", err)
		return
	}
	for _, service := range services {
		fmt.Println(service.Name)
	}

	// A write is a merge patch plus the revision it applies to. The revision is
	// the one the last read returned.
	if len(services) > 0 {
		replicas := 3
		service, err := c.UpdateService(
			context.Background(),
			services[0].ID,
			[]byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)),
			v1.WriteOptions{IfMatch: services[0].Revision},
		)
		if err != nil {
			fmt.Println("update service:", err)
			return
		}
		fmt.Println(service.Revision)
	}
}

// Example_configurationError shows that a remote client refuses to downgrade:
// without a CA bundle the constructor fails with the stable usage code instead
// of opening an unverified connection. It needs no manager and no network.
func Example_configurationError() {
	_, err := client.New(client.Config{
		Endpoint:  "https://mgr:4244",
		TokenFile: "/etc/swarmcracker/api-token",
	})

	var failure *output.Error
	matched := errors.As(err, &failure)
	if !matched {
		fmt.Println("client.New did not return a typed configuration error:", err)
		return
	}
	fmt.Println(matched)
	fmt.Println(failure.Code)

	// Output:
	// true
	// usage
}
