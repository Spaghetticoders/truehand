package main

import (
	"os"

	"github.com/spaghetticoders/truehand/internal/cli"
)

var version = "0.0.0-dev"

func main() {
	cli.Version = version
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
