# pvdu — PVC Disk Usage

Real storage usage of Kubernetes PVCs. Compares requested capacity, PV size, and actual filesystem usage via a parallel directory scanner uploaded to pods.

Built on [dirwalker](https://github.com/NeutryFD/dirwalker) — a parallel, depth-aware directory scanner.

![pvdu demo](assets/demo.gif)

## Install

```bash
make build
```

`make build` produces a single `build/pvdu` binary with both scanner payloads embedded at compile time via `//go:embed`. No external files are needed at runtime.

## Usage

```bash
./build/pvdu -n default
./build/pvdu -n default -o table
./build/pvdu -n monitoring -p data-thanos-receive-0
./build/pvdu -n monitoring -f -i nicolaka/netshoot
./build/pvdu -A --concurrency=10
```

`--all-namespaces` cannot be combined with `--pvc`, positional PVC selection, or `--namespace`.

## Interactive mode

`--tui` opens a PVC browser where you choose which PVCs to scan, instead of scanning them all at once. It needs an interactive terminal and cannot be combined with `-o json`/`yaml`. Press `?` inside the browser for every key binding.

The browser uses the same columns as the CLI tables and hides some name columns on narrow terminals. PVC details identify the temporary scan pod when one was used. `--theme` selects the colour theme: `dracula` (default), `catppuccin`, `nord`, `gruvbox` or `tokyonight`.

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
| `--concurrency` | `-c` | `3` | Max parallel PVC scans (`0` = no limit) |
| `--max-depth` | `-d` | `0` (unlimited) | Scanner directory depth |
| `--exclude` | `-e` | | Paths to exclude from scan (repeatable) |
| `--workers` | `-w` | `0` (auto) | Scanner parallel workers |
| `--context` | | | Kubernetes context |
| `--kubeconfig` | | | Path to kubeconfig file |
| `--output` | `-o` | `default` | Output format: `default`, `table`, `json`, `yaml` |
| `--log-level` | | `warn` | Progress detail: `debug`, `info`, `warn`, `error` |
| `--tui` | | | Browse PVCs interactively and choose which to scan; not combinable with `-o json`/`yaml` |
| `--theme` | | `dracula` | TUI colour theme: `dracula`, `catppuccin`, `nord`, `gruvbox`, `tokyonight` |
| `--version` | | | Print version and exit |

## Output

```bash
./build/pvdu -n default              # Columnar output (no borders, no colors)
./build/pvdu -n default -o table     # ASCII table with borders
./build/pvdu -n default -o json      # JSON array of results
./build/pvdu -n default -o yaml      # YAML output
```

### Columns

The `default`, `table` and TUI views share columns: POD, PVC, PV, STORAGE CLASS, REQUESTED, SIZE, USED, %, STATUS, preceded by NAMESPACE when browsing all namespaces (`-A`). POD identifies the application pod mounting the PVC, including after a temporary-pod scan; an unused PVC shows `-`.

In an interactive terminal the CLI table updates in place below the command, without switching to the alternate screen; redirected output prints one final table with no terminal escape sequences. JSON and YAML retain the actual scan pod and path, use matching field names and return an empty array when there are no PVCs. Failed scans appear in the results and cause a nonzero exit status. Cleanup warnings are included in `warnings` without replacing scan results.

### Status

STATUS moves through `QUEUED`, `PREPARING`, `UPLOADING` and `SCANNING` to `DONE` or `ERROR`; interrupted scans show `CANCELLED`. Temporary-pod scans (`--force`) also show `CREATING POD`, `WAITING POD` and `POD READY`. `UPLOADING` covers sending the scanner into the pod, not PVC data; `SCANNING` begins when the pod reports that the scanner has started.

### Diagnostics and logging

Errors and warnings are never printed between progress updates. They are collected and shown on one line each in a `Diagnostics:` section after the table. `--log-level` controls extra detail: the default `warn` is quiet, `info` adds scan and temporary-pod progress for non-interactive output, and `debug` adds a `Debug:` section after the results with the remote exec target and command, plus the reason an existing-pod scan fell back to a temporary pod.

## Measurement

USED is the sum of apparent file sizes found by the scanner, not allocated disk blocks or filesystem free space. Excludes and depth limits can reduce the total. Deleted-but-open files, snapshots, sparse-file allocation, and filesystem overhead are not measured. The percentage compares this total with requested capacity.

Only whole-volume filesystem mounts are scanned; subpath-only mounts use a temporary pod. Block-mode PVCs are unsupported. ReadWriteOnce fallback stays on the attached node; an in-use ReadWriteOncePod claim cannot be mounted by a second pod. Existing containers must have writable `/tmp` and shell utilities; pvdu never uploads a scanner into application storage. The ineffective `--files` option has been removed.

The scanner binary is built without debug symbols and sent gzip-compressed (about 1.1 MB). Pods without `gunzip` are detected before any data is read and pvdu retries with the uncompressed binary. Upload time depends on the cluster's exec stream throughput, which concurrent scans share, so a higher `--concurrency` can lengthen `UPLOADING` without shortening the run.

## Permissions and safety

Required Kubernetes permissions are `get/list` for PVCs and pods, `get` for PVs and nodes (architecture detection), and `create` for `pods/exec`. Temporary scans additionally need pod `create/delete`. API discovery failures are reported rather than treated as empty results.

Temporary pods have unique names, read-only PVC mounts, no service-account token, dropped Linux capabilities, and bounded lifetime/cleanup. They request 100m CPU and 64Mi memory, with limits of 1 CPU and 512Mi memory. The image's default UID is retained for filesystem compatibility; restrictive volume permissions or admission policies may require an appropriate custom image. A failed cleanup leaves the exact pod name in diagnostics for operator-managed reclamation; pvdu never deletes another invocation's pod by name.

## Supported platforms

CLI builds for Linux and macOS embed separate Linux amd64 and arm64 scanners and select the payload using the target node's architecture. Other pod operating systems/architectures return an explicit error. Cross-compiling the CLI does not change the embedded scanner targets.

## Development

```bash
make build             # Builds scanner, embeds it into pvdu, produces single binary
make test-unit         # Tests that need no cluster, with the race detector
make test-integration  # Full suite; needs a cluster (sets PVDU_E2E=1)
make lint              # golangci-lint
make clean             # Removes build artifacts
```

Generated payloads remain under `cmd/pvdu/scanners/` for subsequent Go commands; `make clean` removes them.
