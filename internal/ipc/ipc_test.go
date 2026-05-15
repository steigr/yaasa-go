package ipc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steigr/yaasa-go/desk"
	"github.com/steigr/yaasa-go/internal/ipc"
)

// stubController is a pure-Go implementation of ipc.Controller used in tests.
// It has no CGo fields, so it is safe to GC without triggering CoreBluetooth
// finaliser crashes that occur with a zero-value *desk.Desk.
type stubController struct {
	info    desk.Info
	address string
}

func (s *stubController) Wake() error  { return nil }
func (s *stubController) MoveUp() error { return nil }
func (s *stubController) MoveDown() error { return nil }
func (s *stubController) Stop() error  { return nil }
func (s *stubController) WaitForHeight(_ context.Context, _ desk.Height, _ desk.Height, _ time.Duration, _ func(desk.Height)) error {
	return nil
}
func (s *stubController) WaitForPreset(_ context.Context, _ int, _ time.Duration, _ time.Duration, _ func(desk.Height)) error {
	return nil
}
func (s *stubController) FetchSitStandTime(_ time.Duration) (desk.SitStandTime, error) {
	return desk.SitStandTime{}, nil
}
func (s *stubController) CurrentHeight(_ time.Duration) (desk.Height, error) {
	return desk.HeightFromMM(720), nil
}
func (s *stubController) SavePreset(_ int) error         { return nil }
func (s *stubController) DeviceInfo() desk.Info          { return s.info }
func (s *stubController) DeviceAddress() string          { return s.address }
func (s *stubController) NotificationsAvailable() bool   { return true }

// testTransport returns a UnixTransport with a short socket path in /tmp.
// Unix socket paths are limited to 104 bytes on macOS; t.TempDir() embeds the
// full test name and can exceed that limit, so we create the directory
// ourselves with a short prefix.
func testTransport(t *testing.T) ipc.UnixTransport {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ys")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return ipc.UnixTransport{Path: filepath.Join(dir, "t.sock")}
}

// testServer starts a server backed by a stubController and registers cleanup.
func testServer(t *testing.T, tr ipc.Transport) *ipc.Server {
	t.Helper()
	ctrl := &stubController{
		info:    desk.Info{DeviceName: "test-desk", Model: "FE60"},
		address: "AA:BB:CC:DD:EE:FF",
	}
	srv, err := ipc.ListenWith(tr, ctrl)
	if err != nil {
		t.Fatalf("ListenWith: %v", err)
	}
	go srv.Serve() //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestDialWithNoServer verifies that DialWith returns an error immediately
// when nothing is listening at the transport address.
func TestDialWithNoServer(t *testing.T) {
	tr := testTransport(t)
	_, err := ipc.DialWith(tr)
	if err == nil {
		t.Fatal("expected error connecting to non-existent server, got nil")
	}
}

// TestListenWithDialWith_Status checks that a client can connect and receive a
// valid status response over a custom transport.
func TestListenWithDialWith_Status(t *testing.T) {
	tr := testTransport(t)
	testServer(t, tr)

	c, err := ipc.DialWith(tr)
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	defer c.Close()

	resp, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !resp.OK {
		t.Fatalf("status not OK: %s", resp.Error)
	}
	if !resp.Connected {
		t.Error("want Connected=true")
	}
	if resp.Address != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("Address = %q, want %q", resp.Address, "AA:BB:CC:DD:EE:FF")
	}
}

// TestListenWithDialWith_Info checks the info command path.
func TestListenWithDialWith_Info(t *testing.T) {
	tr := testTransport(t)
	testServer(t, tr)

	c, err := ipc.DialWith(tr)
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	defer c.Close()

	resp, err := c.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !resp.OK {
		t.Fatalf("info not OK: %s", resp.Error)
	}
	if resp.DeviceName != "test-desk" {
		t.Errorf("DeviceName = %q, want %q", resp.DeviceName, "test-desk")
	}
	if resp.Model != "FE60" {
		t.Errorf("Model = %q, want %q", resp.Model, "FE60")
	}
}

// TestListenWithDialWith_Quit verifies that the quit command closes the
// server's QuitCh channel.
func TestListenWithDialWith_Quit(t *testing.T) {
	tr := testTransport(t)
	srv := testServer(t, tr)

	c, err := ipc.DialWith(tr)
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	defer c.Close()

	c.Quit() //nolint:errcheck — socket closes before full response arrives

	select {
	case <-srv.QuitCh():
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("QuitCh not closed within 2 s after Quit command")
	}
}

// TestListenWithDialWith_UnknownCommand checks that the server returns an
// error response for an unrecognised command.
func TestListenWithDialWith_UnknownCommand(t *testing.T) {
	tr := testTransport(t)
	testServer(t, tr)

	c, err := ipc.DialWith(tr)
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	defer c.Close()

	_, err = c.Do(ipc.Request{Cmd: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

// TestAlreadyRunning verifies that calling ListenWith a second time on the
// same transport address returns an error.
func TestAlreadyRunning(t *testing.T) {
	tr := testTransport(t)
	testServer(t, tr)

	_, err := ipc.ListenWith(tr, &stubController{})
	if err == nil {
		t.Fatal("expected error when binding a second server at the same address, got nil")
	}
}

// TestUnixTransportString checks that String() returns the configured path.
func TestUnixTransportString(t *testing.T) {
	const path = "/tmp/yaasa-test.sock"
	tr := ipc.UnixTransport{Path: path}
	if got := tr.String(); got != path {
		t.Fatalf("String() = %q, want %q", got, path)
	}
}

// TestMultipleClients verifies that the server handles multiple sequential
// connections correctly.
func TestMultipleClients(t *testing.T) {
	tr := testTransport(t)
	testServer(t, tr)

	for i := 0; i < 3; i++ {
		c, err := ipc.DialWith(tr)
		if err != nil {
			t.Fatalf("client %d DialWith: %v", i, err)
		}
		resp, err := c.Status()
		c.Close()
		if err != nil {
			t.Fatalf("client %d Status: %v", i, err)
		}
		if !resp.OK {
			t.Fatalf("client %d: status not OK: %s", i, resp.Error)
		}
	}
}
