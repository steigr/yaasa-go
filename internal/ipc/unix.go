package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UnixTransport implements [Transport] using a Unix domain socket.
// It is the reference IPC transport used by the yaasa CLI.
//
// # Stale-socket handling
//
// A daemon that exits cleanly removes its socket file.  If the process
// crashes the file is left behind.  [UnixTransport.Listen] probes whether a
// live daemon is already answering before binding; if not, it removes any
// stale file so the replacement daemon can bind successfully.
type UnixTransport struct {
	// Path is the filesystem path of the Unix socket file.
	Path string
}

// String returns the socket path.
func (t UnixTransport) String() string { return t.Path }

// Dial opens a client connection to the Unix socket at [UnixTransport.Path].
// Returns an error immediately if no daemon is listening.
func (t UnixTransport) Dial() (net.Conn, error) {
	return net.DialTimeout("unix", t.Path, 2*time.Second)
}

// Listen creates a Unix domain socket at [UnixTransport.Path] and returns a
// [net.Listener].
//
// Returns an explicit error if a live daemon is already running at that path,
// rather than a raw EADDRINUSE.  A stale socket file left by a crashed daemon
// is removed automatically before binding.
func (t UnixTransport) Listen() (net.Listener, error) {
	if conn, err := net.DialTimeout("unix", t.Path, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("a daemon is already running at %s", t.Path)
	}
	_ = os.Remove(t.Path)

	l, err := net.Listen("unix", t.Path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", t.Path, err)
	}
	return l, nil
}

// SocketPath returns the Unix socket path for the given desk address.
// The path lives in the user's runtime directory (or /tmp as fallback).
func SocketPath(addr string) string {
	return filepath.Join(RuntimeDir(), "yaasa-"+safeAddress(addr)+".sock")
}

// RuntimeDir returns the directory used for sockets and daemon state.
func RuntimeDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return base
}

// DefaultAddressPath returns the file used to remember the default desk.
func DefaultAddressPath() string {
	return filepath.Join(RuntimeDir(), "yaasa-default.addr")
}

// DaemonLogPath returns the log path used for auto-started daemon processes.
func DaemonLogPath(addr string) string {
	return filepath.Join(RuntimeDir(), "yaasa-"+safeAddress(addr)+".log")
}

func safeAddress(addr string) string {
	return strings.NewReplacer(":", "-", "/", "-").Replace(addr)
}
