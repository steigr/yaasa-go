BINARY  := yaasa
MODULE  := github.com/steigr/yaasa-go
CMD     := ./cmd/yaasa
CLIB    := ./clib/

.PHONY: build build-linux build-mac-arm build-mac-intel dylib so tidy clean help

## build: build CLI for the current platform
build:
	go build -o $(BINARY) $(CMD)

## build-linux: cross-compile CLI for Linux amd64
build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY)-linux-amd64 $(CMD)

## build-mac-arm: cross-compile CLI for macOS Apple Silicon
build-mac-arm:
	GOOS=darwin GOARCH=arm64 go build -o $(BINARY)-darwin-arm64 $(CMD)

## build-mac-intel: cross-compile CLI for macOS Intel
build-mac-intel:
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY)-darwin-amd64 $(CMD)

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
