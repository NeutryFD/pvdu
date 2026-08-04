# Changelog

All notable changes to pvdu are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.1.0] - 2026-08-04

First release.

### Added

- `--version` flag prints the pvdu version, injected at build time from
  `git describe` (`VERSION`/`LDFLAGS` in the Makefile).
- GitHub Actions CI (KinD) that runs the unit and integration test suites on
  every push and pull request.
- Automated release workflow: pushing a `v*` tag builds the binary and publishes
  a GitHub Release with the `build/pvdu` asset and this changelog's notes.
- Hermetic dirwalker integration contract tests (scanner built from the pinned
  module version).
- `--pvc` flag to filter scans to a single PVC.
- JSON and YAML output formats plus a table format.

### Changed

- Bumped `dirwalker` to v0.3.0 (scanner `human` output field is now opt-in via
  `--human`; the scanner contract tests reflect the new schema).
- Replaced the Bubble Tea TUI with columnar stdout output; live progress moved
  to stderr with per-PVC updating lines.
- Bumped the default per-PVC timeout to 120s.
- Enforced valid temp-pod names, stale temp-pod cleanup, and cleanup on
  interrupt.
- Hardened the in-pod exec command against shell injection (all dynamic values
  passed as positional parameters).

### Removed

- Redundant `usage` subcommand (the root command handles it directly).
- TUI-only types and flags.

### Fixed

- An empty/absent `VERSION` no longer drops the `--version` flag (falls back to
  `dev`).
