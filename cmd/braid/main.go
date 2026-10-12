// Command braid is the Braid integrator entrypoint — the CLI and sandbox binary
// that drives the merge + verification pipeline. During the TS→Go migration this
// is a thin shell; subcommands are wired in as each engine package lands.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("braid %s\n", version)
		return
	}
	fmt.Fprintln(os.Stderr, "braid: the Go integrator is under construction (TS→Go migration).")
	fmt.Fprintln(os.Stderr, "usage: braid version")
}
