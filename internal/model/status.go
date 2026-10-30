package model

type ScanStatus string

const (
	StatusPending     ScanStatus = "pending"
	StatusPreparing   ScanStatus = "preparing"
	StatusScanning    ScanStatus = "scanning"
	StatusDone        ScanStatus = "done"
	StatusError       ScanStatus = "error"
	StatusCreatingPod ScanStatus = "creating pod"
	StatusWaitingPod  ScanStatus = "waiting pod"
	StatusPodReady    ScanStatus = "pod ready"
	StatusUploading   ScanStatus = "uploading"
)
