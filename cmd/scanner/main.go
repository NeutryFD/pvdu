package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/neutry/pvdu/internal/scanner"
)

type progressLine struct {
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Human string `json:"human,omitempty"`
}

type doneLine struct {
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Human string `json:"human"`
}

func main() {
	path := flag.String("path", "/mnt", "directory to scan")
	maxDepth := flag.Int("max-depth", 0, "max directory depth (0 = unlimited)")
	excludes := flag.String("exclude", "", "comma-separated paths to exclude")
	flag.Parse()

	var excludeList []string
	if *excludes != "" {
		excludeList = strings.Split(*excludes, ",")
	}

	enc := json.NewEncoder(os.Stdout)

	progress := func(p string, size int64) {
		_ = enc.Encode(progressLine{
			Type:  "progress",
			Path:  p,
			Size:  size,
			Human: scanner.FormatBytesShort(size),
		})
	}

	total, err := scanner.ScanDirectory(*path, *maxDepth, excludeList, progress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_ = enc.Encode(doneLine{
		Type:  "done",
		Size:  total,
		Human: scanner.FormatBytesShort(total),
	})
}
