BINARY  := yaasa
MODULE  := github.com/steigr/yaasa-go
CMD     := ./cmd/yaasa
CLIB    := ./clib/
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: build build-linux build-mac-arm build-mac-intel release-check release-artifacts dylib so tidy clean help

## build: build CLI for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-linux: cross-compile CLI for Linux amd64
build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(CMD)

## build-mac-arm: cross-compile CLI for macOS Apple Silicon
build-mac-arm:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 $(CMD)

## build-mac-intel: cross-compile CLI for macOS Intel
build-mac-intel:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-amd64 $(CMD)

## release-check: run core quality gates used by release candidates
release-check:
	go test ./...
	go test -race ./...

## release-artifacts: build tested release binaries on this host
release-artifacts: build build-linux build-mac-arm

## dylib: build C shared library for macOS (libdeskcontrol.dylib + .h)
##   Also requires Xcode Command Line Tools (CoreBluetooth via CGo).
##   Usage from C:  #include "libdeskcontrol.h"
dylib:
	go build -buildmode=c-shared -tags clib -o libdeskcontrol.dylib $(CLIB)
	install_name_tool -id @rpath/libdeskcontrol.dylib libdeskcontrol.dylib
	chmod 755 libdeskcontrol.dylib

## so: build C shared library for Linux (libdeskcontrol.so + .h)
so:
	go build -buildmode=c-shared -tags clib -o libdeskcontrol.so $(CLIB)

## tidy: tidy go.mod and go.sum
tidy:
	go mod tidy

## clean: remove built binaries and shared libraries
clean:
	rm -f $(BINARY) $(BINARY)-* libdeskcontrol.dylib libdeskcontrol.so libdeskcontrol.h

## help: print this help
help:
	@grep -E '^## ' Makefile | sed 's/## //'
