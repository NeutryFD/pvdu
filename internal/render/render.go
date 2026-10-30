package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/neutry/pvdu/internal/model"
	"gopkg.in/yaml.v3"
)

// WriteResults preserves the structured schema even when no PVCs were found.
func WriteResults(w io.Writer, format string, results []*model.ScanResult) error {
	if results == nil {
		results = []*model.ScanResult{}
	}
	switch format {
	case "json":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(results)
	case "yaml":
		encoder := yaml.NewEncoder(w)
		defer encoder.Close()
		return encoder.Encode(results)
	case "default", "table":
		_, err := io.WriteString(w, render(results, format == "table"))
		return err
	default:
		return fmt.Errorf("invalid output format %q", format)
	}
}

func JSON(results []*model.ScanResult) string {
	var b strings.Builder
	_ = WriteResults(&b, "json", results)
	return strings.TrimSuffix(b.String(), "\n")
}
func YAML(results []*model.ScanResult) string {
	var b strings.Builder
	_ = WriteResults(&b, "yaml", results)
	return b.String()
}
func Default(results []*model.ScanResult) string { return render(results, false) }
func Table(results []*model.ScanResult) string   { return render(results, true) }
