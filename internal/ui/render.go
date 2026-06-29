package ui

import (
	"encoding/json"
	"fmt"

	"github.com/neutry/pvdu/internal/model"
	"gopkg.in/yaml.v3"
)

func RenderJSON(results []*model.ScanResult) string {
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Sprintf("json marshal error: %v", err)
	}
	return string(b)
}

func RenderYAML(results []*model.ScanResult) string {
	b, err := yaml.Marshal(results)
	if err != nil {
		return fmt.Sprintf("yaml marshal error: %v", err)
	}
	return string(b)
}
