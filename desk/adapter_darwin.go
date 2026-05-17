//go:build darwin

// Direct-on-cbgo implementation of [TinygoAdapter] for macOS.
//
// This file exists so we can apply three customisations that upstream
// tinygo.org/x/bluetooth does not expose:
//
//  1. Set CoreBluetooth's CBCentralManagerOptionRestoreIdentifierKey
//     (cbgo.ManagerOpts.RestoreIdentifier) on the central and peripheral
//     managers, so state restoration works when this library is linked into
//     an app such as Baarsa.  Upstream initialises DefaultAdapter at package
//     init with nil options and offers no setter.
//
//  2. Make Enable() idempotent.  Upstream blocks forever on poweredChan if
//     Enable() is called twice (e.g. from both Scan() and Connect()).  We
//     enter the same code path from both, and the clib bindings may call in
//     repeatedly.
//
//  3. Always clear the powered-on channel after success or timeout, instead
//     of leaving it non-nil so the "already calling Enable" guard fires
//     spuriously on the next call.
//
// All three are reflected in docs/tinygo-bluetooth-patches.md as a single
// upstream PR proposal.  The Linux/Windows path (adapter_other.go) still
// uses the upstream stack directly.
//
// The implementation is modelled on tinygo's own adapter_darwin.go /
// gap_darwin.go / gattc_darwin.go to stay behaviourally compatible — the
// concrete shapes (delegate base structs, per-call channel handshakes, the
// 300 ms post-discovery settle) match what the rest of the desk package was
// written against.

package desk

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tinygo-org/cbgo"
)

// ── UUIDs ────────────────────────────────────────────────────────────────────
//
// The desk service (0xFE60) and its three characteristics, plus the standard
// Device Information Service and its string characteristics.  All stored as
// full-128-bit UUIDs so comparison against [cbgo.UUID.String] (which always
// returns the long form) is trivial.

const (
	uuidServiceDesk  = "0000fe60-0000-1000-8000-00805f9b34fb"
	uuidCharCommand  = "0000fe61-0000-1000-8000-00805f9b34fb"
	uuidCharResponse = "0000fe62-0000-1000-8000-00805f9b34fb"
	uuidCharName     = "0000fe63-0000-1000-8000-00805f9b34fb"

	uuidServiceDIS  = "0000180a-0000-1000-8000-00805f9b34fb"
	uuidDISMfg      = "00002a29-0000-1000-8000-00805f9b34fb"
	uuidDISModel    = "00002a24-0000-1000-8000-00805f9b34fb"
	uuidDISSerial   = "00002a25-0000-1000-8000-00805f9b34fb"
	uuidDISFWRev    = "00002a26-0000-1000-8000-00805f9b34fb"
	uuidDISHWRev    = "00002a27-0000-1000-8000-00805f9b34fb"
	uuidDISSWRev    = "00002a28-0000-1000-8000-00805f9b34fb"
)

var (
	cbServiceDesk  = cbgo.MustParseUUID(uuidServiceDesk)
	cbServiceDIS   = cbgo.MustParseUUID(uuidServiceDIS)
)

func uuidEq(a cbgo.UUID, want string) bool {
	return strings.EqualFold(a.String(), want)
}

// ── manager (singleton) ──────────────────────────────────────────────────────
//
// CoreBluetooth wants a single CBCentralManager per process; we follow the
// same model as bluetooth.DefaultAdapter.  The manager is constructed lazily
// on first use so package init has no side effects (avoids a CoreBluetooth
// background-dispatch SIGABRT observed before the original tinygo Connect
// returned).

type darwinManager struct {
	cm  cbgo.CentralManager
	cmd *centralDelegate

	// Enable lifecycle.
	enableMu   sync.Mutex
	enabled    bool
	poweredCh  chan struct{}

	// Scan lifecycle.  Only one scan runs at a time — the desk package
	// only calls Scan from a single goroutine.
	scanMu     sync.Mutex
	scanFilter cbgo.UUID                                  // service UUID we filter on
	scanCB     func(addr, name string, rssi int16)

	// Pending Connect requests, keyed by peripheral identifier string.  The
	// channel receives the connected peripheral or a connect error.
	pendingMu sync.Mutex
	pending   map[string]chan connectResult
}

type connectResult struct {
	p   cbgo.Peripheral
	err error
}

var (
	mgrOnce sync.Once
	mgr     *darwinManager
)

func getManager() *darwinManager {
	mgrOnce.Do(func() {
		m := &darwinManager{
			cm: cbgo.NewCentralManager(&cbgo.ManagerOpts{
				RestoreIdentifier: "yaasa.ble.central",
			}),
			pending: make(map[string]chan connectResult),
		}
		m.cmd = &centralDelegate{m: m}
		m.cm.SetDelegate(m.cmd)
		mgr = m
	})
	return mgr
}

// ensureEnabled is the idempotent equivalent of upstream Adapter.Enable.  Safe
// to call from multiple goroutines and multiple times.
func (m *darwinManager) ensureEnabled() error {
	m.enableMu.Lock()
	defer m.enableMu.Unlock()

	if m.cm.State() == cbgo.ManagerStatePoweredOn {
		m.enabled = true
		return nil
	}
	if m.enabled {
		// We already saw a powered-on transition once; the manager may
		// be momentarily not powered (e.g. user toggled Bluetooth) —
		// fall through and wait again.
	}

	m.poweredCh = make(chan struct{}, 1)
	defer func() { m.poweredCh = nil }()

	// The delegate is set in getManager(); the powered-on event arrives via
	// CentralManagerDidUpdateState.  If the manager is already powered on
	// by the time we get here, the delegate will not fire again — handle
	// that with one more state check after registering the channel.
	if m.cm.State() == cbgo.ManagerStatePoweredOn {
		m.enabled = true
		return nil
	}

	select {
	case <-m.poweredCh:
		m.enabled = true
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("timeout enabling CentralManager")
	}
}

// ── centralDelegate ──────────────────────────────────────────────────────────

type centralDelegate struct {
	cbgo.CentralManagerDelegateBase
	m *darwinManager
}

func (d *centralDelegate) CentralManagerDidUpdateState(cm cbgo.CentralManager) {
	if cm.State() != cbgo.ManagerStatePoweredOn {
		return
	}
	d.m.enableMu.Lock()
	ch := d.m.poweredCh
	d.m.enableMu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (d *centralDelegate) DidDiscoverPeripheral(cm cbgo.CentralManager, p cbgo.Peripheral, ad cbgo.AdvFields, rssi int) {
	d.m.scanMu.Lock()
	cb := d.m.scanCB
	filter := d.m.scanFilter
	d.m.scanMu.Unlock()
	if cb == nil {
		return
	}
	if len(filter) > 0 {
		match := false
		for _, u := range ad.ServiceUUIDs {
			if strings.EqualFold(u.String(), filter.String()) {
				match = true
				break
			}
		}
		if !match {
			return
		}
	}
	cb(p.Identifier().String(), ad.LocalName, int16(rssi))
}

func (d *centralDelegate) DidConnectPeripheral(cm cbgo.CentralManager, p cbgo.Peripheral) {
	d.completePending(p, nil)
}

func (d *centralDelegate) DidFailToConnectPeripheral(cm cbgo.CentralManager, p cbgo.Peripheral, err error) {
	d.completePending(p, err)
}

func (d *centralDelegate) DidDisconnectPeripheral(cm cbgo.CentralManager, p cbgo.Peripheral, err error) {
	// If a connect was in flight, unblock it with the disconnect error.
	// Steady-state disconnects (no pending connect) are dropped silently —
	// the desk package detects loss-of-connection lazily on the next write.
	d.completePending(p, fmt.Errorf("disconnected: %w", err))
}

func (d *centralDelegate) completePending(p cbgo.Peripheral, err error) {
	id := p.Identifier().String()
	d.m.pendingMu.Lock()
	ch, ok := d.m.pending[id]
	if ok {
		delete(d.m.pending, id)
	}
	d.m.pendingMu.Unlock()
	if ok {
		ch <- connectResult{p: p, err: err}
	}
}

// ── TinygoAdapter (public) ───────────────────────────────────────────────────

// TinygoAdapter implements [Adapter] on darwin directly via cbgo.
//
// Despite the name (kept for cross-platform API stability), on darwin this
// type does NOT use tinygo.org/x/bluetooth — see the file header for why.
type TinygoAdapter struct{}

// Scan starts a BLE scan filtered to the FE60 service and calls cb for each
// discovered desk.  It stops after timeout elapses.
func (TinygoAdapter) Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error {
	m := getManager()
	if err := m.ensureEnabled(); err != nil {
		return fmt.Errorf("enable BLE adapter: %w", err)
	}

	m.scanMu.Lock()
	if m.scanCB != nil {
		m.scanMu.Unlock()
		return errors.New("scan already in progress")
	}
	m.scanCB = cb
	m.scanFilter = cbServiceDesk
	m.scanMu.Unlock()

	defer func() {
		m.scanMu.Lock()
		m.scanCB = nil
		m.scanFilter = nil
		m.scanMu.Unlock()
	}()

	m.cm.Scan([]cbgo.UUID{cbServiceDesk}, &cbgo.CentralManagerScanOpts{
		AllowDuplicates: false,
	})
	time.Sleep(timeout)
	m.cm.StopScan()
	return nil
}

// Connect performs the same two-phase setup as the tinygo-backed adapter:
// discover services + characteristics + DIS strings, wait 300 ms for the
// Lierda BLE module to settle, then return a Connection.  Notification setup
// is the caller's responsibility (ConnectWith handles it).
func (TinygoAdapter) Connect(addr string, timeout time.Duration) (Connection, Info, error) {
	m := getManager()
	if err := m.ensureEnabled(); err != nil {
		return nil, Info{}, fmt.Errorf("enable BLE adapter: %w", err)
	}

	cbAddr, err := cbgo.ParseUUID(addr)
	if err != nil {
		return nil, Info{}, fmt.Errorf("parse address %q: %w", addr, err)
	}
	prphs := m.cm.RetrievePeripheralsWithIdentifiers([]cbgo.UUID{cbAddr})
	if len(prphs) == 0 {
		return nil, Info{}, fmt.Errorf("no peripheral with identifier %s — has it been seen by a recent scan?", addr)
	}
	p := prphs[0]
	id := p.Identifier().String()

	ch := make(chan connectResult, 1)
	m.pendingMu.Lock()
	m.pending[id] = ch
	m.pendingMu.Unlock()

	m.cm.Connect(p, nil)

	var res connectResult
	select {
	case res = <-ch:
	case <-time.After(timeout):
		m.cm.CancelConnect(p)
		m.pendingMu.Lock()
		delete(m.pending, id)
		m.pendingMu.Unlock()
		return nil, Info{}, fmt.Errorf("connect to %s: timed out after %s", addr, timeout)
	}
	if res.err != nil {
		return nil, Info{}, fmt.Errorf("connect to %s: %w", addr, res.err)
	}
	if res.p.State() != cbgo.PeripheralStateConnected {
		return nil, Info{}, fmt.Errorf("connect to %s: peripheral did not enter connected state", addr)
	}

	conn := newDarwinConnection(m.cm, res.p)

	// ── Phase 1: discovery ──────────────────────────────────────────────
	if err := conn.discoverAll(); err != nil {
		_ = conn.Disconnect()
		return nil, Info{}, err
	}

	// ── Phase 2: stabilisation delay ────────────────────────────────────
	//
	// The Lierda LSD4BT-E95ASTD001 module rejects CCCD writes if any FE61
	// command is sent before the CCCD subscription.  Wait briefly for the
	// link to settle so the caller's subsequent EnableNotifications call
	// is not racing the GATT discovery completion.
	time.Sleep(300 * time.Millisecond)

	return conn, conn.info, nil
}

// ── connection ───────────────────────────────────────────────────────────────

// darwinConnection implements [Connection] for darwin.  All async cbgo
// callbacks land in peripheralDelegate and are routed through per-operation
// channels.  Sequential discovery/read operations share a single readCh by
// convention (only one read in flight at a time, just like upstream tinygo).
type darwinConnection struct {
	cm   cbgo.CentralManager
	prph cbgo.Peripheral
	pd   *peripheralDelegate

	cmdChar  cbgo.Characteristic
	respChar cbgo.Characteristic
	haveCmd  bool
	haveResp bool

	// Notification callback for the response characteristic.  Set by
	// EnableNotifications; consulted in DidUpdateValueForCharacteristic.
	notifyMu sync.RWMutex
	notifyCB func([]byte)

	// Async-operation channels.  Each is non-nil exactly while a call is
	// in flight; the delegate sends on whichever is set and drops events
	// for completed operations.
	servicesCh chan error
	charsCh    chan error
	readCh     chan error
	notifyCh   chan error

	// Cached discovered info — populated during discoverAll, returned by
	// Connect via the conn.info field.
	info Info
}

func newDarwinConnection(cm cbgo.CentralManager, p cbgo.Peripheral) *darwinConnection {
	c := &darwinConnection{
		cm:         cm,
		prph:       p,
		servicesCh: make(chan error, 1),
		charsCh:    make(chan error, 1),
		readCh:     make(chan error, 1),
		notifyCh:   make(chan error, 1),
	}
	c.pd = &peripheralDelegate{c: c}
	p.SetDelegate(c.pd)
	return c
}

// discoverAll discovers FE60 + DIS, walks both, populates the characteristic
// handles, reads the DIS strings + FE63 device name.
func (c *darwinConnection) discoverAll() error {
	c.prph.DiscoverServices([]cbgo.UUID{cbServiceDesk, cbServiceDIS})
	if err := waitWithTimeout(c.servicesCh, 10*time.Second, "discover services"); err != nil {
		return err
	}

	for _, svc := range c.prph.Services() {
		switch {
		case uuidEq(svc.UUID(), uuidServiceDesk):
			if err := c.discoverDeskChars(svc); err != nil {
				return err
			}
		case uuidEq(svc.UUID(), uuidServiceDIS):
			c.discoverInfoChars(svc) // best effort
		}
	}
	if !c.haveCmd {
		return errors.New("FE61 (command) characteristic not found")
	}
	if !c.haveResp {
		return errors.New("FE62 (response) characteristic not found")
	}
	return nil
}

func (c *darwinConnection) discoverDeskChars(svc cbgo.Service) error {
	c.prph.DiscoverCharacteristics(nil, svc)
	if err := waitWithTimeout(c.charsCh, 10*time.Second, "discover FE60 characteristics"); err != nil {
		return err
	}
	for _, ch := range svc.Characteristics() {
		switch {
		case uuidEq(ch.UUID(), uuidCharCommand):
			c.cmdChar = ch
			c.haveCmd = true
		case uuidEq(ch.UUID(), uuidCharResponse):
			c.respChar = ch
			c.haveResp = true
		case uuidEq(ch.UUID(), uuidCharName):
			c.info.DeviceName = c.readString(ch)
		}
	}
	return nil
}

func (c *darwinConnection) discoverInfoChars(svc cbgo.Service) {
	c.prph.DiscoverCharacteristics(nil, svc)
	if err := waitWithTimeout(c.charsCh, 10*time.Second, "discover DIS characteristics"); err != nil {
		return
	}
	for _, ch := range svc.Characteristics() {
		switch {
		case uuidEq(ch.UUID(), uuidDISMfg):
			c.info.Manufacturer = c.readString(ch)
		case uuidEq(ch.UUID(), uuidDISModel):
			c.info.Model = c.readString(ch)
		case uuidEq(ch.UUID(), uuidDISSerial):
			c.info.Serial = c.readString(ch)
		case uuidEq(ch.UUID(), uuidDISFWRev):
			c.info.FirmwareRev = c.readString(ch)
		case uuidEq(ch.UUID(), uuidDISHWRev):
			c.info.HardwareRev = c.readString(ch)
		case uuidEq(ch.UUID(), uuidDISSWRev):
			c.info.SoftwareRev = c.readString(ch)
		}
	}
}

// readString reads ch synchronously and returns the value as a string.  Errors
// are dropped because every caller treats them as "field not present".
func (c *darwinConnection) readString(ch cbgo.Characteristic) string {
	c.prph.ReadCharacteristic(ch)
	if err := waitWithTimeout(c.readCh, 10*time.Second, "read characteristic"); err != nil {
		return ""
	}
	return string(ch.Value())
}

// WriteCommand sends data on FE61 as write-without-response.  Mirrors the
// tinygo implementation's flow-control: poll CanSendWriteWithoutResponse with
// 15 ms gaps until the BLE module accepts the write or a 10 s budget elapses.
func (c *darwinConnection) WriteCommand(data []byte) error {
	if !c.haveCmd {
		return errors.New("FE61 characteristic not discovered")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !c.prph.CanSendWriteWithoutResponse() {
		if time.Now().After(deadline) {
			return errors.New("WriteCommand: timed out waiting for buffer space")
		}
		time.Sleep(15 * time.Millisecond)
	}
	c.prph.WriteCharacteristic(data, c.cmdChar, false)
	return nil
}

// EnableNotifications subscribes to FE62 with a single direct CCCD write —
// matches the Bleak behaviour that is empirically known to work on the Yaasa
// firmware.  Do NOT add a "disable first" step here; see the file header of
// the old tinygo.go (now adapter_other.go) and docs/protocol.md for the
// reason.
func (c *darwinConnection) EnableNotifications(cb func([]byte)) error {
	if !c.haveResp {
		return errors.New("FE62 characteristic not discovered")
	}
	c.notifyMu.Lock()
	c.notifyCB = cb
	c.notifyMu.Unlock()

	c.prph.SetNotify(cb != nil, c.respChar)
	return waitWithTimeout(c.notifyCh, 10*time.Second, "enable notifications")
}

// ReadResponse performs a synchronous GATT Read on FE62.  On Yaasa firmware
// FE62 is notify-only and returns "Read Not Permitted" — kept as a no-op
// fallback for compatibility with the Connection interface; callers tolerate
// the error.
func (c *darwinConnection) ReadResponse() ([]byte, error) {
	if !c.haveResp {
		return nil, errors.New("FE62 characteristic not discovered")
	}
	c.prph.ReadCharacteristic(c.respChar)
	if err := waitWithTimeout(c.readCh, 10*time.Second, "read FE62"); err != nil {
		return nil, err
	}
	v := c.respChar.Value()
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// Disconnect closes the GATT connection.  CoreBluetooth's CancelConnect on a
// connected peripheral disconnects it (the API is reused for both "abort
// connecting" and "disconnect").
func (c *darwinConnection) Disconnect() error {
	c.cm.CancelConnect(c.prph)
	return nil
}

// ── peripheralDelegate ───────────────────────────────────────────────────────

type peripheralDelegate struct {
	cbgo.PeripheralDelegateBase
	c *darwinConnection
}

func (pd *peripheralDelegate) DidDiscoverServices(prph cbgo.Peripheral, err error) {
	deliver(pd.c.servicesCh, err)
}

func (pd *peripheralDelegate) DidDiscoverCharacteristics(prph cbgo.Peripheral, svc cbgo.Service, err error) {
	deliver(pd.c.charsCh, err)
}

func (pd *peripheralDelegate) DidUpdateValueForCharacteristic(prph cbgo.Peripheral, chr cbgo.Characteristic, err error) {
	// One CoreBluetooth callback covers BOTH read responses and notifications.
	// Tinygo handles this by always invoking the callback (if any) and also
	// signalling the per-characteristic readChan (if any).  We do the same:
	// the readCh receiver only exists during an in-flight Read, so spurious
	// signals are dropped by the non-blocking send.
	if pd.c.haveResp && uuidEq(chr.UUID(), uuidCharResponse) && err == nil {
		pd.c.notifyMu.RLock()
		cb := pd.c.notifyCB
		pd.c.notifyMu.RUnlock()
		if cb != nil {
			// Copy the value so the callback owns a stable slice — cbgo
			// reuses the underlying NSData buffer across callbacks.
			v := chr.Value()
			out := make([]byte, len(v))
			copy(out, v)
			cb(out)
		}
	}
	deliver(pd.c.readCh, err)
}

func (pd *peripheralDelegate) DidUpdateNotificationState(prph cbgo.Peripheral, chr cbgo.Characteristic, err error) {
	deliver(pd.c.notifyCh, err)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// deliver does a non-blocking send so callbacks for already-completed
// operations are dropped instead of deadlocking the delegate goroutine.
func deliver(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

// waitWithTimeout waits for ch to deliver an error (or nil) and returns it.
// Wraps the operation name into the timeout error message.
func waitWithTimeout(ch chan error, timeout time.Duration, op string) error {
	select {
	case err := <-ch:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timeout on %s after %s", op, timeout)
	}
}
