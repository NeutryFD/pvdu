package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NeutryFD/dirwalker"
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

func RenderDefault(results []*model.ScanResult) string {
	var b strings.Builder

	colWidths := map[string]int{
		"ns":   12,
		"pod":  24,
		"pvc":  24,
		"path": 24,
		"req":  12,
		"pv":   12,
		"used": 12,
		"pct":  8,
	}

	for _, r := range results {
		if len(r.Namespace) > colWidths["ns"] {
			colWidths["ns"] = len(r.Namespace)
		}
		podName := r.PodName
		if podName == "" {
			podName = "-"
		}
		if len(podName) > colWidths["pod"] {
			colWidths["pod"] = len(podName)
		}
		if len(r.PVCName) > colWidths["pvc"] {
			colWidths["pvc"] = len(r.PVCName)
		}
		if len(r.ScanPath) > colWidths["path"] {
			colWidths["path"] = len(r.ScanPath)
		}
	}

	rpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return s + strings.Repeat(" ", w-len(s))
	}

	lpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return strings.Repeat(" ", w-len(s)) + s
	}

	fmt.Fprintf(&b, "%s  %s  %s  %s  %s  %s  %s  %s\n",
		rpad("NAMESPACE", colWidths["ns"]),
		rpad("POD", colWidths["pod"]),
		rpad("PVC", colWidths["pvc"]),
		rpad("PATH", colWidths["path"]),
		lpad("REQUESTED", colWidths["req"]),
		lpad("PV SIZE", colWidths["pv"]),
		lpad("USED", colWidths["used"]),
		lpad("%", colWidths["pct"]))

	var errs []string
	for _, r := range results {
		podStr := r.PodName
		if podStr == "" {
			podStr = "-"
		}
		pathStr := r.ScanPath
		if pathStr == "" {
			pathStr = "-"
		}
		req := r.RequestedStr
		if req == "" {
			req = "-"
		}
		pvStr := "-"
		if r.PVBytes > 0 {
			pvStr = dirwalker.FormatBytesShort(r.PVBytes)
		}
		usedStr := "-"
		pctStr := "-"
		if r.Status == model.StatusDone && r.UsedBytes >= 0 {
			usedStr = dirwalker.FormatBytesShort(r.UsedBytes)
			if r.RequestedBytes > 0 {
				pct := float64(r.UsedBytes) / float64(r.RequestedBytes) * 100
				pctStr = fmt.Sprintf("%.0f%%", pct)
			}
		} else if r.Status == model.StatusError {
			usedStr = "ERROR"
			errs = append(errs, fmt.Sprintf("  %s/%s: %s", r.Namespace, r.PVCName, r.Error))
		}

		fmt.Fprintf(&b, "%s  %s  %s  %s  %s  %s  %s  %s\n",
			rpad(r.Namespace, colWidths["ns"]),
			rpad(podStr, colWidths["pod"]),
			rpad(r.PVCName, colWidths["pvc"]),
			rpad(pathStr, colWidths["path"]),
			lpad(req, colWidths["req"]),
			lpad(pvStr, colWidths["pv"]),
			lpad(usedStr, colWidths["used"]),
			lpad(pctStr, colWidths["pct"]))
	}

	if len(errs) > 0 {
		fmt.Fprintf(&b, "\nErrors:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "%s\n", e)
		}
	}

	return b.String()
}

func RenderTable(results []*model.ScanResult) string {
	var b strings.Builder

	colWidths := map[string]int{
		"ns":   12,
		"pod":  24,
		"pvc":  24,
		"path": 24,
		"req":  12,
		"pv":   12,
		"used": 12,
		"pct":  8,
	}

	for _, r := range results {
		if len(r.Namespace) > colWidths["ns"] {
			colWidths["ns"] = len(r.Namespace)
		}
		podName := r.PodName
		if podName == "" {
			podName = "-"
		}
		if len(podName) > colWidths["pod"] {
			colWidths["pod"] = len(podName)
		}
		if len(r.PVCName) > colWidths["pvc"] {
			colWidths["pvc"] = len(r.PVCName)
		}
		if len(r.ScanPath) > colWidths["path"] {
			colWidths["path"] = len(r.ScanPath)
		}
	}

	row := func(cols ...string) {
		fmt.Fprintf(&b, "|")
		for _, c := range cols {
			fmt.Fprintf(&b, " %s |", c)
		}
		fmt.Fprintf(&b, "\n")
	}

	sep := func() {
		fmt.Fprintf(&b, "+")
		for _, w := range []int{colWidths["ns"], colWidths["pod"], colWidths["pvc"], colWidths["path"], colWidths["req"], colWidths["pv"], colWidths["used"], colWidths["pct"]} {
			fmt.Fprintf(&b, "%s+", strings.Repeat("-", w+2))
		}
		fmt.Fprintf(&b, "\n")
	}

	rpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return s + strings.Repeat(" ", w-len(s))
	}

	lpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return strings.Repeat(" ", w-len(s)) + s
	}

	center := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		left := (w - len(s)) / 2
		right := w - len(s) - left
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
	}

	sep()
	row(
		center("NAMESPACE", colWidths["ns"]),
		rpad("POD", colWidths["pod"]),
		center("PVC", colWidths["pvc"]),
		rpad("PATH", colWidths["path"]),
		rpad("REQUESTED", colWidths["req"]),
		rpad("PV SIZE", colWidths["pv"]),
		rpad("USED", colWidths["used"]),
		rpad("%", colWidths["pct"]),
	)
	sep()

	var errs []string
	for _, r := range results {
		podStr := r.PodName
		if podStr == "" {
			podStr = "-"
		}
		pathStr := r.ScanPath
		if pathStr == "" {
			pathStr = "-"
		}
		req := r.RequestedStr
		if req == "" {
			req = "-"
		}
		pvStr := "-"
		if r.PVBytes > 0 {
			pvStr = dirwalker.FormatBytesShort(r.PVBytes)
		}
		usedStr := "-"
		pctStr := "-"
		if r.Status == model.StatusDone && r.UsedBytes >= 0 {
			usedStr = dirwalker.FormatBytesShort(r.UsedBytes)
			if r.RequestedBytes > 0 {
				pct := float64(r.UsedBytes) / float64(r.RequestedBytes) * 100
				pctStr = fmt.Sprintf("%.0f%%", pct)
			}
		} else if r.Status == model.StatusError {
			usedStr = "ERROR"
			errs = append(errs, fmt.Sprintf("  %s/%s: %s", r.Namespace, r.PVCName, r.Error))
		}

		row(
			rpad(r.Namespace, colWidths["ns"]),
			rpad(podStr, colWidths["pod"]),
			rpad(r.PVCName, colWidths["pvc"]),
			rpad(pathStr, colWidths["path"]),
			lpad(req, colWidths["req"]),
			lpad(pvStr, colWidths["pv"]),
			lpad(usedStr, colWidths["used"]),
			lpad(pctStr, colWidths["pct"]),
		)
	}
	sep()

	if len(errs) > 0 {
		fmt.Fprintf(&b, "\nErrors:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "%s\n", e)
		}
	}

	return b.String()
}
