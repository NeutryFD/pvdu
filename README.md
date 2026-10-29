# pvdu — PVC Disk Usage

Real storage usage of Kubernetes PVCs. Compares requested capacity, PV size, and actual filesystem usage via a parallel directory scanner uploaded to pods.

Built on [dirwalker](https://github.com/NeutryFD/dirwalker) — a parallel, depth-aware directory scanner.

## Demo

![pvdu demo](assets/demo.gif)

## Quick start

```bash
make build
./build/pvdu -n default
./build/pvdu -n default -o table
./build/pvdu -n monitoring -p data-thanos-receive-0
./build/pvdu -n monitoring -f -i nicolaka/netshoot
./build/pvdu -A --concurrency=10
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | `default` | Kubernetes namespace |
| `--all-namespaces` | `-A` | | Scan all namespaces |
| `--pvc` | `-p` | | Filter by PVC name |
| `--force` | `-f` | | Auto-create temp pod when exec into existing pod fails |
| `--image` | `-i` | `alpine:3.23` | Image for temp pods |
| `--timeout` | `-t` | `120s` | Budget for each attempt; forced fallback gets a fresh budget |
| `--cleanup-timeout` | | `10s` | Independent temporary-pod deletion budget |
| `--concurrency` | `-c` | `3` | Max parallel PVC scans |
| `--max-depth` | `-d` | `0` (unlimited) | Scanner directory depth |
| `--exclude` | `-e` | | Paths to exclude from scan (repeatable) |
| `--workers` | `-w` | `0` (auto) | Scanner parallel workers |
| `--context` | | | Kubernetes context |
| `--kubeconfig` | | | Path to kubeconfig file |
| `--output` | `-o` | `default` | Output format: `default`, `table`, `json`, `yaml` |
| `--log-level` | | | Log level: `debug`, `info`, `warn`, `error` |
| `--version` | | | Print version and exit |

## Output formats

```bash
./build/pvdu -n default              # Columnar output (no borders, no colors)
./build/pvdu -n default -o table     # ASCII table with borders
./build/pvdu -n default -o json      # JSON array of results
./build/pvdu -n default -o yaml      # YAML output
```

The `default` format shows each PVC on one line with columns: NAMESPACE, POD, PVC, PATH, REQUESTED, PV SIZE, USED, %. Progress goes to stderr; redirected output contains no terminal escape sequences. JSON and YAML use matching field names and return an empty array when there are no PVCs. Failed scans appear in the results and cause a nonzero exit status. Cleanup warnings are included in `warnings` without replacing scan results.

## Measurement and permissions

USED is the sum of apparent file sizes found by the scanner, not allocated disk blocks or filesystem free space. Excludes and depth limits can reduce the total. Deleted-but-open files, snapshots, sparse-file allocation, and filesystem overhead are not measured. The percentage compares this total with requested capacity.

Only whole-volume filesystem mounts are scanned; subpath-only mounts use a temporary pod. Block-mode PVCs are unsupported. ReadWriteOnce fallback stays on the attached node; an in-use ReadWriteOncePod claim cannot be mounted by a second pod. Existing containers must have writable `/tmp` and shell utilities; pvdu never uploads a scanner into application storage. The ineffective `--files` option has been removed.

Required Kubernetes permissions are `get/list` for PVCs and pods, `get` for PVs and nodes (architecture detection), and `create` for `pods/exec`. Temporary scans additionally need pod `create/delete`. API discovery failures are reported rather than treated as empty results. `--all-namespaces` cannot be combined with `--pvc`, positional PVC selection, or `--namespace`.

Temporary pods have unique names, read-only PVC mounts, no service-account token, dropped Linux capabilities, and bounded lifetime/cleanup. They request 100m CPU and 64Mi memory, with limits of 1 CPU and 512Mi memory. The image's default UID is retained for filesystem compatibility; restrictive volume permissions or admission policies may require an appropriate custom image. A failed cleanup leaves the exact pod name in diagnostics for operator-managed reclamation; pvdu never deletes another invocation's pod by name.

## Supported platforms

CLI builds for Linux and macOS embed separate Linux amd64 and arm64 scanners and select the payload using the target node's architecture. Other pod operating systems/architectures return an explicit error. Cross-compiling the CLI does not change the embedded scanner targets.

## Build

```bash
make build    # Builds scanner, embeds it into pvdu, produces single binary
make test     # Builds scanner, runs unit + integration tests
make clean    # Removes build artifacts
```

`make build` produces a single `build/pvdu` binary with both scanner payloads embedded at compile time via `//go:embed`. No external files are needed at runtime. Generated payloads remain under `cmd/pvdu/scanners/` for subsequent Go commands; `make clean` removes them.
