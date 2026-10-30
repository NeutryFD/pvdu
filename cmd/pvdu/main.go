package main

import (
	"embed"
	"os"

	"github.com/neutry/pvdu/internal/cli"
	"github.com/neutry/pvdu/internal/scanner"
)

//go:embed scanners/dirwalker-linux-*
var scanners embed.FS

func main() {
	scanner.Binaries = make(map[string][]byte)
	for _, arch := range []string{"amd64", "arm64"} {
		binary, err := scanners.ReadFile("scanners/dirwalker-linux-" + arch)
		if err == nil {
			scanner.Binaries[arch] = binary
		}
	}
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
