package desk_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/steigr/yaasa-go/desk"
	"github.com/steigr/yaasa-go/internal/protocol"
)

// ── stub adapter ─────────────────────────────────────────────────────────────

// stubAdapter is a pure-Go implementation of desk.Adapter for tests.
// It has no CGo fields and never touches real Bluetooth hardware.
type stubAdapter struct {
	// scanResults are emitted by Scan in order.
	scanResults []scanResult
	// connectInfo is returned by Connect.
	connectInfo desk.Info
	// connectErr is returned by Connect if non-nil.
	connectErr error
	// conn is the connection returned by Connect.
	conn *stubConnection
}

type scanResult struct {
	addr, name string
	rssi       int16
}

func (a *stubAdapter) Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error {
	for _, r := range a.scanResults {
		cb(r.addr, r.name, r.rssi)
	}
	return nil
}

func (a *stubAdapter) Connect(addr string, timeout time.Duration) (desk.Connection, desk.Info, error) {
	if a.connectErr != nil {
		return nil, desk.Info{}, a.connectErr
	}
	if a.conn == nil {
		a.conn = &stubConnection{}
	}
	return a.conn, a.connectInfo, nil
}

// ── stub connection ───────────────────────────────────────────────────────────

// stubConnection is a pure-Go implementation of desk.Connection for tests.
type stubConnection struct {
	mu           sync.Mutex
	written      [][]byte        // all packets sent via WriteCommand
	notifyCb     func([]byte)    // registered by EnableNotifications
	readResponse []byte          // returned by ReadResponse
	readErr      error
	disconnected bool
}

func (c *stubConnection) WriteCommand(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	c.written = append(c.written, cp)
	return nil
}

func (c *stubConnection) EnableNotifications(cb func([]byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifyCb = cb
	return nil
}

func (c *stubConnection) ReadResponse() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.readResponse, nil
}

func (c *stubConnection) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnected = true
	return nil
}

// emit delivers a raw FE62 notification to the registered handler.
func (c *stubConnection) emit(buf []byte) {
	c.mu.Lock()
	cb := c.notifyCb
	c.mu.Unlock()
	if cb != nil {
		cb(buf)
	}
}

// emitHeight delivers a height notification packet for h mm.
func (c *stubConnection) emitHeight(mm float64) {
	h := desk.HeightFromMM(mm)
	b := h.MillimetreBytes()
	// checksum = 0x01 + 0x03 + b[0] + b[1] + 0x00
	cs := byte(0x01) + byte(0x03) + b[0] + b[1] + 0x00
	pkt := []byte{0xF2, 0xF2, 0x01, 0x03, b[0], b[1], 0x00, cs, 0x7E}
	c.emit(pkt)
}

// lastWritten returns the last packet sent via WriteCommand, or nil.
func (c *stubConnection) lastWritten() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.written) == 0 {
		return nil
	}
	return c.written[len(c.written)-1]
}

// ── helpers ───────────────────────────────────────────────────────────────────

func connectStub(t *testing.T, info desk.Info) (*desk.Desk, *stubConnection) {
	t.Helper()
	conn := &stubConnection{}
	a := &stubAdapter{connectInfo: info, conn: conn}
	d, err := desk.ConnectWith(a, "AA:BB:CC:DD:EE:FF", 5*time.Second)
	if err != nil {
		t.Fatalf("ConnectWith: %v", err)
	}
	t.Cleanup(func() { d.Disconnect() })
	return d, conn
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestScanWith verifies that ScanWith calls the adapter's Scan and forwards
// results to the callback.
func TestScanWith(t *testing.T) {
	a := &stubAdapter{
		scanResults: []scanResult{
			{"AA:BB:CC:DD:EE:01", "Desk A", -60},
			{"AA:BB:CC:DD:EE:02", "Desk B", -75},
		},
	}
	var got []string
	err := desk.ScanWith(a, time.Second, func(addr, name string, rssi int16) {
		got = append(got, addr)
	})
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0] != "AA:BB:CC:DD:EE:01" || got[1] != "AA:BB:CC:DD:EE:02" {
		t.Fatalf("unexpected addresses: %v", got)
	}
}

// TestConnectWith_PopulatesAddress checks that ConnectWith stores the address
// string on the returned Desk.
func TestConnectWith_PopulatesAddress(t *testing.T) {
	d, _ := connectStub(t, desk.Info{})
	if d.Address != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("Address = %q, want %q", d.Address, "AA:BB:CC:DD:EE:FF")
	}
}

// TestConnectWith_PopulatesInfo checks that ConnectWith copies device Info
// from the adapter's Connect result onto the Desk.
func TestConnectWith_PopulatesInfo(t *testing.T) {
	info := desk.Info{DeviceName: "TestDesk", Model: "FE60", Serial: "SN-001"}
	d, _ := connectStub(t, info)
	if d.Info.DeviceName != "TestDesk" {
		t.Fatalf("DeviceName = %q, want %q", d.Info.DeviceName, "TestDesk")
	}
	if d.Info.Model != "FE60" {
		t.Fatalf("Model = %q, want %q", d.Info.Model, "FE60")
	}
}

// TestConnectWith_SendsWakeAndPrewarm verifies that ConnectWith sends, in
// order, a wake command (opcode 0x00) and a RequestHeightLimits pre-warm
// (opcode 0x07) — both after subscribing to FE62 notifications.
func TestConnectWith_SendsWakeAndPrewarm(t *testing.T) {
	_, conn := connectStub(t, desk.Info{})
	wantWake := protocol.MakeCommand(0x00)
	wantWarm := protocol.MakeCommand(0x07)

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) < 2 {
		t.Fatalf("expected at least 2 writes (wake + prewarm), got %d", len(conn.written))
	}
	got1 := conn.written[len(conn.written)-2]
	got2 := conn.written[len(conn.written)-1]
	if string(got1) != string(wantWake) {
		t.Fatalf("second-last write = % X, want wake (% X)", got1, wantWake)
	}
	if string(got2) != string(wantWarm) {
		t.Fatalf("last write = % X, want prewarm 0x07 (% X)", got2, wantWarm)
	}
}

// TestConnectWith_RegistersNotificationHandler checks that ConnectWith
// registers a notification handler that updates the height cache.
func TestConnectWith_RegistersNotificationHandler(t *testing.T) {
	d, conn := connectStub(t, desk.Info{})

	if _, ok := d.LastKnownHeight(); ok {
		t.Fatal("expected no cached height before any notification")
	}

	conn.emitHeight(720)

	h, ok := d.LastKnownHeight()
	if !ok {
		t.Fatal("expected cached height after notification, got none")
	}
	if h.MM() != 720 {
		t.Fatalf("height = %.1f mm, want 720.0 mm", h.MM())
	}
}

// TestConnectWith_AdapterError checks that ConnectWith propagates a connection
// error from the adapter.
func TestConnectWith_AdapterError(t *testing.T) {
	wantErr := errors.New("BLE adapter not available")
	a := &stubAdapter{connectErr: wantErr}
	_, err := desk.ConnectWith(a, "AA:BB:CC:DD:EE:FF", time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// TestDisconnect checks that Disconnect calls the connection's Disconnect.
func TestDisconnect(t *testing.T) {
	conn := &stubConnection{}
	a := &stubAdapter{conn: conn}
	d, err := desk.ConnectWith(a, "AA:BB:CC:DD:EE:FF", time.Second)
	if err != nil {
		t.Fatalf("ConnectWith: %v", err)
	}
	if err := d.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if !conn.disconnected {
		t.Fatal("expected connection to be marked disconnected")
	}
}

// TestPollHeightDirect checks that PollHeightDirect reads from the connection
// and updates the height cache.
func TestPollHeightDirect(t *testing.T) {
	d, conn := connectStub(t, desk.Info{})

	// Craft a height notification for 750 mm and set it as the poll response.
	h := desk.HeightFromMM(750)
	b := h.MillimetreBytes()
	cs := byte(0x01) + byte(0x03) + b[0] + b[1] + 0x00
	pkt := []byte{0xF2, 0xF2, 0x01, 0x03, b[0], b[1], 0x00, cs, 0x7E}
	conn.mu.Lock()
	conn.readResponse = pkt
	conn.mu.Unlock()

	if err := d.PollHeightDirect(); err != nil {
		t.Fatalf("PollHeightDirect: %v", err)
	}
	got, ok := d.LastKnownHeight()
	if !ok {
		t.Fatal("expected height after poll, got none")
	}
	if got.MM() != 750 {
		t.Fatalf("height = %.1f, want 750.0", got.MM())
	}
}

// TestAddHeightListener checks that listeners registered with AddHeightListener
// receive height notifications and can be cancelled.
func TestAddHeightListener(t *testing.T) {
	d, conn := connectStub(t, desk.Info{})

	var received []desk.Height
	var mu sync.Mutex
	cancel := d.AddHeightListener(func(h desk.Height) {
		mu.Lock()
		received = append(received, h)
		mu.Unlock()
	})

	conn.emitHeight(700)
	conn.emitHeight(720)

	cancel()
	conn.emitHeight(740) // should not be received after cancel

	mu.Lock()
	got := len(received)
	mu.Unlock()
	if got != 2 {
		t.Fatalf("listener received %d heights, want 2", got)
	}
}

// TestDeviceInfo and TestDeviceAddress check the Controller interface methods.
func TestDeviceInfo(t *testing.T) {
	info := desk.Info{DeviceName: "My Desk", Manufacturer: "Jiecang"}
	d, _ := connectStub(t, info)
	got := d.DeviceInfo()
	if got.DeviceName != "My Desk" || got.Manufacturer != "Jiecang" {
		t.Fatalf("DeviceInfo() = %+v", got)
	}
}

func TestDeviceAddress(t *testing.T) {
	d, _ := connectStub(t, desk.Info{})
	if got := d.DeviceAddress(); got != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("DeviceAddress() = %q, want %q", got, "AA:BB:CC:DD:EE:FF")
	}
}
