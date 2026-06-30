package main

import (
	_ "embed"
	"os"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
)

//go:embed dirwalker
var scannerBinary []byte

func main() {
	k8s.ScannerBinary = scannerBinary
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
