package main

import (
	"embed"
	"os"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
)

//go:embed pvdu-scanner
var scannerBin embed.FS

func main() {
	data, err := scannerBin.ReadFile("pvdu-scanner")
	if err == nil {
		k8s.ScannerBinary = data
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
