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
	Namespace      string
	PVCName        string
	RequestedBytes int64
	RequestedStr   string
	PVBytes        int64
	UsedBytes      int64
	Status         ScanStatus
	Error          string
}

type ScanProgress struct {
	PVCName string
	Path    string
	Size    int64
	Human   string
	Done    bool
}

type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatJSON  OutputFormat = "json"
	FormatYAML  OutputFormat = "yaml"
	FormatWide  OutputFormat = "wide"
)

type ProgressUpdate struct {
	PVCName string
	Path    string
	Size    int64
	Done    bool
	Err     string
}

type DiscoveredMsg struct {
	Count int
}

type ScanDoneMsg struct{}
