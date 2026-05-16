package desk

import (
	"sync"
	"time"

	"github.com/steigr/yaasa-go/internal/protocol"
)

// Info holds device-information service values read on connect.
type Info struct {
	Model        string
	Manufacturer string
	Serial       string
	FirmwareRev  string
	HardwareRev  string
	SoftwareRev  string
	DeviceName   string // FE63 characteristic
}

// Desk represents an active BLE connection to a Jiecang FE60 standing desk.
// Obtain one via [Connect] (using [TinygoAdapter]) or [ConnectWith] (any
// [Adapter]).  All exported methods are safe for concurrent use.
type Desk struct {
	Info    Info
	Address string // BLE address string, e.g. "AA:BB:CC:DD:EE:FF"

	conn    Connection
	verbose bool

	// notifyErr is non-nil when FE62 notification setup failed at connect time.
	notifyErr error

	// lastHeight caches the most recently received FE62 height notification.
	// Updated by handleNotification; read by CurrentHeight as a no-movement
	// fast path so the desk doesn't need to move just to answer a height query.
	lastHeightMu sync.RWMutex
	lastHeight   Height
	hasHeight    bool

	// Notification fanout — subscribers registered via AddHeightListener.
	listenersMu sync.Mutex
	listeners   []func(Height)

	// Sit/stand time cache — updated by every 0xA2 FE62 notification.
	lastStatsMu      sync.RWMutex
	lastStats        SitStandTime
	hasStats         bool
	statsListenersMu sync.Mutex
	statsListeners   []func(SitStandTime)
}

// ConnectOption is a functional option for [Connect] / [ConnectWith].
type ConnectOption func(*Desk)

// WithVerbose enables logging of raw BLE packets.
func WithVerbose(v bool) ConnectOption {
	return func(d *Desk) { d.verbose = v }
}

// ScanWith starts a BLE scan using the given adapter and calls cb for every
// device that advertises the FE60 service.  It stops after timeout.
func ScanWith(a Adapter, timeout time.Duration, cb func(addr, name string, rssi int16)) error {
	return a.Scan(timeout, cb)
}

// Scan starts a BLE scan using [TinygoAdapter] and calls cb for every device
// that advertises the FE60 service.  It stops after timeout.
func Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error {
	return ScanWith(TinygoAdapter{}, timeout, cb)
}

// ConnectWith establishes a BLE connection to the desk at addrStr using the
// provided adapter, discovers the required characteristics, and enables FE62
// notifications.
//
// The adapter handles all GATT discovery and any adapter-specific stabilisation
// delays.  ConnectWith registers the height/stat notification handler and sends
// the initial wake pulse after notification setup.
//
// Subscribing to FE62 BEFORE any FE61 write is required: the Lierda BLE
// module (LSD4BT-E95ASTD001) rejects CCCD writes with ATT error 0x11
// ("Insufficient Resources") if a command is sent to FE61 first.
func ConnectWith(a Adapter, addrStr string, connectTimeout time.Duration, opts ...ConnectOption) (*Desk, error) {
	d := &Desk{}
	for _, o := range opts {
		o(d)
	}

	conn, info, err := a.Connect(addrStr, connectTimeout)
	if err != nil {
		return nil, err
	}
	d.Address = addrStr
	d.Info = info
	d.conn = conn

	if err := conn.EnableNotifications(d.handleNotification); err != nil {
		d.notifyErr = err
		d.verboseLogf("[BLE] FE62 notification setup failed (%v) — will still attempt height feedback", err)
	}

	// Wake the desk after subscribing so any height notifications triggered
	// by the wake are captured by the now-registered handler.
	wakeCmd := protocol.MakeCommand(0x00)
	d.verboseLogf("[BLE tx] (wake post-subscribe) % X", wakeCmd)
	_ = conn.WriteCommand(wakeCmd)

	// Pre-warm the height cache: send a RequestHeightLimits (0x07).  On the
	// reference firmware this triggers a delayed (~4 s) opcode-0x01 height
	// notification as a side effect, so the cache is populated before the
	// first user query — making CurrentHeight() an instant cache hit instead
	// of a 5 s slow path.  The write is fire-and-forget; if it fails the
	// slow path in CurrentHeight still works.
	statusCmd := protocol.MakeCommand(0x07)
	d.verboseLogf("[BLE tx] (cache pre-warm) % X", statusCmd)
	_ = conn.WriteCommand(statusCmd)

	return d, nil
}

// Connect establishes a BLE connection using [TinygoAdapter].
// See [ConnectWith] for full documentation.
func Connect(addrStr string, connectTimeout time.Duration, opts ...ConnectOption) (*Desk, error) {
	return ConnectWith(TinygoAdapter{}, addrStr, connectTimeout, opts...)
}

// NotificationsAvailable always returns true — we attempt to use FE62
// regardless of the CCCD write result.
//
// Some Jiecang/Yaasa firmware variants respond with ATT error 0x11
// ("Insufficient Resources") when the CCCD write is acknowledged, but still
// begin streaming notifications.  Treating that error as fatal would break
// height/move/monitor on those desks.  Commands that need height data will
// naturally time out if notifications genuinely never arrive.
func (d *Desk) NotificationsAvailable() bool { return true }

// NotifyError returns the error from the notification setup, or nil.
func (d *Desk) NotifyError() error { return d.notifyErr }

// handleNotification is called by the BLE stack for every FE62 notification.
// It decodes the packet and broadcasts to all registered listeners.
func (d *Desk) handleNotification(buf []byte) {
	d.verboseLogf("[BLE rx] % X", buf)
	for _, packet := range protocol.DecodeResponses(buf) {
		// ── height (opcode 0x01) ──────────────────────────────────────────
		if h, ok := decodeHeightPayload(packet.Opcode, packet.Payload); ok {
			d.lastHeightMu.Lock()
			d.lastHeight = h
			d.hasHeight = true
			d.lastHeightMu.Unlock()

			d.listenersMu.Lock()
			ls := make([]func(Height), len(d.listeners))
			copy(ls, d.listeners)
			d.listenersMu.Unlock()
			for _, l := range ls {
				l(h)
			}
			continue
		}

		// ── sit/stand time (opcode 0xA2) ─────────────────────────────────
		if s, ok := decodeSitStandTime(packet.Opcode, packet.Payload); ok {
			d.lastStatsMu.Lock()
			d.lastStats = s
			d.hasStats = true
			d.lastStatsMu.Unlock()

			d.statsListenersMu.Lock()
			ls := make([]func(SitStandTime), len(d.statsListeners))
			copy(ls, d.statsListeners)
			d.statsListenersMu.Unlock()
			for _, l := range ls {
				l(s)
			}
			continue
		}
	}
}

func decodeHeightNotification(buf []byte) (Height, bool) {
	opcode, payload, ok := protocol.DecodeResponse(buf)
	if !ok {
		return 0, false
	}
	return decodeHeightPayload(opcode, payload)
}

func decodeHeightPayload(opcode byte, payload []byte) (Height, bool) {
	// Opcode 0x01 = height report, 3-byte payload.
	//
	// Empirically confirmed on a Yaasa Frame Expert (Jiecang FE60 / Lierda
	// LSD4BT-E95ASTD001):
	//   payload[0:2] big-endian uint16 = height in whole millimetres
	//   payload[2]   appears reserved / always 0x00
	//
	// The uplift-ble documentation says payload[1:3] in tenths of mm, which
	// would give the same bytes on Uplift hardware but is incorrect here.
	// Ground-truth: desk at known 793 mm sends [0x03, 0x19, 0x00];
	// 0x0319 = 793 ✓  (vs 0x1900 / 10 = 640 mm, which is wrong).
	if opcode != 0x01 || len(payload) < 3 {
		return 0, false
	}
	raw := (uint16(payload[0]) << 8) | uint16(payload[1])
	return HeightFromMM(float64(raw)), true
}

// LastKnownHeight returns the most recently received height notification and
// true, or (0, false) if no notification has arrived since connect.
func (d *Desk) LastKnownHeight() (Height, bool) {
	d.lastHeightMu.RLock()
	defer d.lastHeightMu.RUnlock()
	return d.lastHeight, d.hasHeight
}

// PollHeightDirect issues a GATT Read on FE62 and feeds the response into the
// normal height-listener fanout.  This is a fallback for adapters that reject
// CCCD notification subscription (ATT error 0x11): a Read does not require a
// CCCD write, so it bypasses that restriction entirely.
//
// Returns an error only if the GATT Read itself fails.
func (d *Desk) PollHeightDirect() error {
	buf, err := d.conn.ReadResponse()
	if err != nil {
		return err
	}
	if len(buf) > 0 {
		d.handleNotification(buf)
	}
	return nil
}

// AddHeightListener registers cb to be called for every height notification.
// Returns a cancel function that unregisters the listener.
// Safe to call concurrently.
func (d *Desk) AddHeightListener(cb func(Height)) (cancel func()) {
	d.listenersMu.Lock()
	d.listeners = append(d.listeners, cb)
	idx := len(d.listeners) - 1
	d.listenersMu.Unlock()

	return func() {
		d.listenersMu.Lock()
		defer d.listenersMu.Unlock()
		if idx < len(d.listeners) {
			d.listeners = append(d.listeners[:idx], d.listeners[idx+1:]...)
		}
	}
}

// DeviceInfo returns the desk's device-information fields.
// It is part of the [ipc.Controller] interface.
func (d *Desk) DeviceInfo() Info { return d.Info }

// DeviceAddress returns the desk's BLE address string.
// It is part of the [ipc.Controller] interface.
func (d *Desk) DeviceAddress() string { return d.Address }

// Disconnect closes the BLE connection.
func (d *Desk) Disconnect() error {
	return d.conn.Disconnect()
}
