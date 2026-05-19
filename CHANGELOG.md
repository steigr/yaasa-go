# Changelog

All notable changes to this project are documented in this file.

## [1.0.0] - 2026-05-19

### Added

- First stable `yaasa-go` release baseline.
- Build-time CLI version metadata (`version`, `commit`, `buildDate`) and `yaasa version` command.
- Release automation targets in `Makefile`:
  - `release-check` (tests + race detector)
  - `release-artifacts` (host-verified artifact set)
- New release documentation and quick-start project `README.md`.

### Quality checks used for this release candidate

- `go test ./...`
- `go test -race ./...`
- `go run ./cmd/yaasa --help`
- `make build build-linux build-mac-arm`

### Known release limitation

- `make build-mac-intel` may fail on Apple Silicon hosts without an amd64-compatible CoreBluetooth/cgo cross-compile toolchain.

