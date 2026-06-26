# pvdu — PVC Disk Usage

> **WIP** — Work in progress. APIs and behavior may change.

Real storage usage of Kubernetes PVCs. Compares requested capacity, PV size, and actual filesystem usage via a parallel `WalkDir` scanner.

## Quick start

```bash
make build
./build/pvdu usage -n default
./build/pvdu usage pvc data -n default --force
./build/pvdu usage -A --concurrency=10
```

See `./build/pvdu usage --help` for all flags.
