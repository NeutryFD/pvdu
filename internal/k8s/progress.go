package k8s

import (
	"context"

	"github.com/neutry/pvdu/internal/model"
)

type scanObserverKey struct{}

// WithScanObserver attaches a synchronous per-scan observer. An empty status
// carries debug-only detail, such as the remote exec command.
func WithScanObserver(ctx context.Context, observer func(model.ScanStatus, string)) context.Context {
	return context.WithValue(ctx, scanObserverKey{}, observer)
}

// NotifyScanProgress runs the observer on the scan owner's goroutine.
func NotifyScanProgress(ctx context.Context, status model.ScanStatus, detail string) {
	if observer, ok := ctx.Value(scanObserverKey{}).(func(model.ScanStatus, string)); ok {
		observer(status, detail)
	}
}
