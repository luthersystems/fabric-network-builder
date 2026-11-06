// Command fabric-network-builder generates and runs local Hyperledger
// Fabric networks. See README.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/luthersystems/fabric-network-builder/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	stop()
	os.Exit(code)
}
