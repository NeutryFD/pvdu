package render

import (
	"fmt"
	"strings"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
)

// Column defines a PVC table column for both the CLI and TUI. Width zero
// means a flexible name column; HidePriority controls narrow TUI layouts.
type Column struct {
	Title        string
	Width        int
	HidePriority int
	RightAlign   bool
	Value        func(model.ScanResult) string
}

// Columns returns the shared ordered table schema. Each call owns its slice.
func Columns(namespace bool) []Column {
	columns := []Column{
		{Title: "NAMESPACE", Value: func(r model.ScanResult) string { return OrDash(r.Namespace) }},
		{Title: "POD", HidePriority: 3, Value: func(r model.ScanResult) string { return OrDash(r.MountingPodName) }},
		{Title: "PVC", Value: func(r model.ScanResult) string { return OrDash(r.PVCName) }},
		{Title: "PV", HidePriority: 1, Value: func(r model.ScanResult) string { return OrDash(r.PVName) }},
		{Title: "STORAGE CLASS", HidePriority: 2, Value: func(r model.ScanResult) string { return OrDash(r.StorageClass) }},
		{Title: "REQUESTED", Width: 9, RightAlign: true, Value: func(r model.ScanResult) string { return OrDash(r.RequestedStr) }},
		{Title: "SIZE", Width: 9, RightAlign: true, Value: func(r model.ScanResult) string { return FormatBytes(r.PVBytes) }},
		{Title: "USED", Width: 9, RightAlign: true, Value: func(r model.ScanResult) string {
			if r.Status != model.StatusDone {
				return "-"
			}
			return dirwalker.FormatBytesShort(r.UsedBytes)
		}},
		{Title: "%", Width: 5, RightAlign: true, Value: func(r model.ScanResult) string {
			if r.Status != model.StatusDone {
				return "-"
			}
			return UsagePercent(r)
		}},
		{Title: "STATUS", Width: 14, Value: StatusLabel},
	}
	if !namespace {
		return columns[1:]
	}
	return columns
}

// StatusLabel formats a scan state without animation or terminal styling.
func StatusLabel(r model.ScanResult) string {
	switch {
	case r.Status == "":
		return "-"
	case r.Status == model.StatusPending:
		return "QUEUED"
	case r.Status == model.StatusError && r.Error == "interrupted":
		return "CANCELLED"
	default:
		return strings.ToUpper(string(r.Status))
	}
}

// UsagePercent reports usage relative to the requested capacity.
func UsagePercent(r model.ScanResult) string {
	if r.RequestedBytes <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(r.UsedBytes)/float64(r.RequestedBytes))
}

// FormatBytes formats a known capacity, or a dash when unavailable.
func FormatBytes(n int64) string {
	if n <= 0 {
		return "-"
	}
	return dirwalker.FormatBytesShort(n)
}

// OrDash substitutes a dash for an absent table value.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
