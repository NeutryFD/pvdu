# pvdu — PVC Disk Usage

Real storage usage of Kubernetes PVCs. Compares requested capacity, PV size, and actual filesystem usage via a parallel `WalkDir` scanner uploaded to pods.

## Quick start

```bash
make build
./build/pvdu usage -n default
./build/pvdu usage pvc data -n default --force
./build/pvdu usage -A --concurrency=10
```

## Usage flags

| Flag | Short | Description |
|------|-------|-------------|
| `--namespace` | `-n` | Kubernetes namespace |
| `--all-namespaces` | `-A` | Scan all namespaces |
| `--force` | `-f` | Auto-create temp pod, skip confirmation |
| `--image` | `-i` | Image for temp pods (default: `alpine:latest`) |
| `--timeout` | `-t` | Timeout for pod creation + scan |
| `--concurrency` | `-c` | Max parallel PVC scans (default: 3) |
| `--max-depth` | `-d` | Scanner directory depth (0 = unlimited) |
| `--exclude` | `-e` | Paths to exclude (repeatable) |
| `--workers` | `-w` | Scanner parallel workers (0 = auto) |
| `--files` | | Report individual file sizes in scan output |
| `--output` | `-o` | Output format: `table`, `json`, `yaml`, `wide` (default: `table`) |
| `--no-tui` | | Skip Bubble Tea UI, print final table |
| `--context` | | Kubernetes context |
| `--kubeconfig` | | Path to kubeconfig |

## Build

`make build` produces `build/dirwalker` (standalone scanner) and `build/pvdu` (with scanner embedded).
