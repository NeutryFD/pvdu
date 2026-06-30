package model

type ScanStatus string

const (
	StatusPending  ScanStatus = "pending"
	StatusScanning ScanStatus = "scanning"
	StatusDone     ScanStatus = "done"
	StatusError    ScanStatus = "error"
	StatusSkipped  ScanStatus = "skipped"
)

type ScanResult struct {
	Namespace      string     `json:"namespace"`
	PVCName        string     `json:"pvc_name"`
	PodName        string     `json:"pod_name,omitempty"`
	ScanPath       string     `json:"scan_path"`
	RequestedBytes int64      `json:"requested_bytes"`
	RequestedStr   string     `json:"requested_str"`
	PVBytes        int64      `json:"pv_bytes"`
	UsedBytes      int64      `json:"used_bytes"`
	Status         ScanStatus `json:"status"`
	Error          string     `json:"error,omitempty"`
}

type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatJSON  OutputFormat = "json"
	FormatYAML  OutputFormat = "yaml"
)
