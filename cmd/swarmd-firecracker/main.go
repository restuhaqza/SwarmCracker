// swarmd-firecracker - SwarmKit agent with Firecracker executor support
//
// This is a modified SwarmKit agent that integrates SwarmCracker's Firecracker
// microVM executor. It can join any SwarmKit cluster and run tasks as isolated
// microVMs instead of containers.
package main

import (
	"os"

	"github.com/moby/swarmkit/v2/log"
	"github.com/urfave/cli/v2"
)

var (
	// Version is set by build flags (-X main.Version=...)
	Version = "0.0.0-dev"
)

const (
	defaultStateDir  = "/var/lib/swarmkit"
	defaultJoinRetry = 3
)

func main() {
	app := cli.NewApp()
	app.Name = "swarmd-firecracker"
	app.Usage = "SwarmKit agent with Firecracker microVM executor"
	app.Version = Version
	app.Flags = newAppFlags()
	app.Action = runAgent

	if err := app.Run(os.Args); err != nil {
		log.L.Fatalf("%v", err)
	}
}
