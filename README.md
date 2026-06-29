# pvdu — PVC Disk Usage

Real storage usage of Kubernetes PVCs. Compares requested capacity, PV size, and actual filesystem usage via a parallel directory scanner uploaded to pods.

Built on [dirwalker](https://github.com/NeutryFD/dirwalker) — a parallel, depth-aware directory scanner.

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

## Output formats

```
./build/pvdu -n default           # TUI with live progress
./build/pvdu -n default --no-tui  # Table to stdout
./build/pvdu -n default -o json   # JSON array of results
./build/pvdu -n default -o yaml   # YAML output
```

## Build

```bash
make build    # builds both dirwalker (scanner) and pvdu binaries
make test     # runs unit + integration tests
```

`make build` downloads the [dirwalker](https://github.com/NeutryFD/dirwalker) module automatically — no manual clone needed.
