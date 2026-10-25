// Package main provides a Grafana Loki observability storage retention
// and lifecycle management CLI for routine DevOps disk cleanup.
package main

import (
	"os"

	"github.com/blackbirdworks/loki-prune/cli"
)

func main() {
	if code := runMain(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func runMain(args []string) int {
	if err := cli.Run(args, os.Stdout, os.Stderr); err != nil {
		return 1
	}

	return 0
}
