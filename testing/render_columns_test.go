package pvdu_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/neutry/pvdu/internal/engine"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/render"
	"github.com/neutry/pvdu/internal/tui"
	"k8s.io/client-go/kubernetes"
)

func TestCLIAndTUITableColumnsMatchAfterForcedScan(t *testing.T) {
	for _, namespace := range []string{"ns", ""} {
		name := namespace
		if name == "" {
			name = "all namespaces"
		}
		t.Run(name, func(t *testing.T) {
			e := tuiEngine(func(_ context.Context, pod string) (*model.ScanResult, error) {
				if pod != "temporary-scanner" {
					return nil, errors.New("read-only filesystem")
				}
				return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1 << 30}, nil
			}, engine.Dependencies{
				CreatePod: func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error) {
					return kube.PodRef{Name: "temporary-scanner"}, nil
				},
				WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
					return nil
				},
				DeletePod: func(context.Context, kubernetes.Interface, string, kube.PodRef) error { return nil },
			})
			d := newTUIDriver(t, tui.Config{Engine: e, Namespace: namespace})
			d.send(tea.WindowSizeMsg{Width: 200, Height: 30})
			d.until("list", contains("data-alpha"))
			d.press("f", "y")
			d.until("forced scan", contains("DONE", "10%"))

			// Run through the same engine for CLI results, including fallback.
			pvc := tuiPVCs()[0]
			result := engine.NewResult(pvc)
			e.ScanForced(t.Context(), 0, result, pvc, nil)
			if result.PodName != "temporary-scanner" || result.MountingPodName != "alpha-0" {
				t.Fatalf("scan and mounting pods lost: %+v", result)
			}
			wantHeader := "POD PVC PV STORAGE CLASS REQUESTED SIZE USED % STATUS"
			if namespace == "" {
				wantHeader = "NAMESPACE " + wantHeader
			}
			var tuiHeader, tuiRow []string
			for _, line := range strings.Split(d.screen(), "\n") {
				if strings.Contains(line, "REQUESTED") {
					tuiHeader = strings.Fields(line)
				}
				if strings.Contains(line, "data-alpha") {
					tuiRow = strings.Fields(line)
				}
			}
			if strings.Join(tuiHeader, " ") != wantHeader {
				t.Fatalf("TUI header: %v", tuiHeader)
			}
			for _, format := range []string{"default", "table"} {
				var output bytes.Buffer
				if err := render.WriteResults(
					&output, format, []*model.ScanResult{result}, render.TableOptions{ShowNamespace: namespace == ""},
				); err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(output.String(), "\n") {
					fields := strings.Fields(strings.ReplaceAll(line, "|", " "))
					if strings.Contains(line, "REQUESTED") && !slices.Equal(fields, tuiHeader) {
						t.Fatalf("%s header %v differs from TUI %v", format, fields, tuiHeader)
					}
					if strings.Contains(line, "data-alpha") && !slices.Equal(fields, tuiRow) {
						t.Fatalf("%s row %v differs from TUI %v", format, fields, tuiRow)
					}
				}
			}
		})
	}
}
