package main

import (
	"os"
	"path/filepath"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
)

func main() {
	scannerPath := filepath.Join("build", "dirwalker")
	data, err := os.ReadFile(scannerPath)
	if err == nil {
		k8s.ScannerBinary = data
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
