package main

import (
	"embed"
	"os"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
)

//go:embed scanners/dirwalker-linux-*
var scanners embed.FS

func main() {
	k8s.ScannerBinaries = make(map[string][]byte)
	for _, arch := range []string{"amd64", "arm64"} {
		binary, err := scanners.ReadFile("scanners/dirwalker-linux-" + arch)
		if err == nil {
			k8s.ScannerBinaries[arch] = binary
		}
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
