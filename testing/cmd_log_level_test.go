package pvdu_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/cli"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func runForceProgressCommand(t *testing.T, output io.Writer) {
	t.Helper()
	deps := cli.Dependencies{
		BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
			return fake.NewSimpleClientset(), &rest.Config{}, nil
		},
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
			return []kube.PVCInfo{{Namespace: "monitoring", Name: strings.Repeat("claim", 20), Mounts: []kube.PodMount{{PodName: "app", MountPath: "/data"}}}}, nil
		},
		CreatePod: func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error) {
			return kube.PodRef{Name: "temporary-scanner"}, nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		DeletePod:       func(context.Context, kubernetes.Interface, string, kube.PodRef) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			if pod == "app" {
				return nil, errors.New("read-only filesystem")
			}
			scanner.NotifyProgress(ctx, "", "exec monitoring/temporary-scanner container=scanner argv=[sh -c scanner]")
			scanner.NotifyProgress(ctx, model.StatusUploading, "")
			used, _, err := scanner.Stream(ctx, func(_ context.Context, out, _ io.Writer) error {
				_, err := io.WriteString(out, "{\"type\":\"scan-start\"}\n{\"type\":\"done\",\"size\":42}\n")
				return err
			})
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: used}, err
		},
	}
	c := cli.NewCommand(deps)
	c.SetOut(output)
	c.SetErr(output)
	c.SetArgs([]string{"-n", "monitoring", "-f", "--log-level=debug"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestLogLevelControlsProgressOutput(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantLog bool
	}{
		{name: "default warn is quiet"},
		{name: "info shows progress", args: []string{"--log-level=info"}, wantLog: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			deps := cli.Dependencies{
				BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
					return fake.NewSimpleClientset(), &rest.Config{}, nil
				},
				ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
					return []kube.PVCInfo{{
						Namespace: "ns", Name: "claim",
						Mounts: []kube.PodMount{{PodName: "app", ContainerName: "app", MountPath: "/data"}},
					}}, nil
				},
				UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
					return &model.ScanResult{Status: model.StatusDone}, nil
				},
			}
			c := cli.NewCommand(deps)
			c.SetOut(&stdout)
			c.SetErr(&stderr)
			c.SetArgs(append([]string{"-n", "ns"}, tt.args...))
			if err := c.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			gotLog := strings.Contains(stderr.String(), "scanning app:/data")
			if gotLog != tt.wantLog {
				t.Fatalf("progress log present = %t, want %t; stderr: %s", gotLog, tt.wantLog, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatal("expected scan results on stdout")
			}
		})
	}
}
