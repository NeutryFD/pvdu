package model

type ScanResult struct {
	// Discovery metadata for human-readable tables; PodName remains the scan pod.
	MountingPodName string     `json:"-" yaml:"-"`
	PVName          string     `json:"-" yaml:"-"`
	StorageClass    string     `json:"-" yaml:"-"`
	Namespace       string     `json:"namespace" yaml:"namespace"`
	PVCName         string     `json:"pvc_name" yaml:"pvc_name"`
	PodName         string     `json:"pod_name,omitempty" yaml:"pod_name,omitempty"`
	ScanPath        string     `json:"scan_path" yaml:"scan_path"`
	RequestedBytes  int64      `json:"requested_bytes" yaml:"requested_bytes"`
	RequestedStr    string     `json:"requested_str" yaml:"requested_str"`
	PVBytes         int64      `json:"pv_bytes" yaml:"pv_bytes"`
	UsedBytes       int64      `json:"used_bytes" yaml:"used_bytes"`
	Status          ScanStatus `json:"status" yaml:"status"`
	Error           string     `json:"error,omitempty" yaml:"error,omitempty"`
	Warnings        []string   `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}
