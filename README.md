# yaasa-go

Go CLI and library for controlling Yaasa/Jiecang FE60 standing desks over BLE.

## Release status

- Current release target: `v1.0.0`
- Stability goal: production-ready CLI for day-to-day desk control
- Supported runtime hosts: macOS (Apple Silicon verified), Linux (CLI build verified)

## CLI quick start

Build locally:

```bash
make build
```

Show available commands:

```bash
./yaasa --help
```

Show build version metadata:

```bash
./yaasa version
```

Common usage:

```bash
./yaasa scan
./yaasa height
./yaasa move 720
./yaasa preset 1
./yaasa stop
```

## Release quality gates

Run the checks used for release candidates:

```bash
make release-check
```

Build release artifacts verified on this host:

```bash
make release-artifacts VERSION=v1.0.0
```

Optional extra build target (requires suitable cross-compile setup):

```bash
make build-mac-intel VERSION=v1.0.0
```

## Project layout

- `cmd/yaasa`: CLI application
- `desk`: BLE desk control package
- `internal/ipc`: local daemon client/server protocol
- `internal/protocol`: FE60 packet framing and parser
- `docs/protocol.md`: reverse-engineered FE60 protocol notes

