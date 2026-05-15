package desk

import (
	"fmt"
	"sync"
	"time"

	"github.com/steigr/yaasa-go/internal/protocol"
	"tinygo.org/x/bluetooth"
)

var (
	serviceUUID  = bluetooth.New16BitUUID(0xFE60)
	charCommand  = bluetooth.New16BitUUID(0xFE61) // write-only
	charResponse = bluetooth.New16BitUUID(0xFE62) // notify
	charName     = bluetooth.New16BitUUID(0xFE63) // read — device name string
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
// Obtain one via Connect. All exported methods are safe for concurrent use.
type Desk struct {
	Info    Info
	Address bluetooth.Address

	device   bluetooth.Device
	cmdChar  bluetooth.DeviceCharacteristic
	respChar bluetooth.DeviceCharacteristic // FE62 — stored, notifications enabled after discovery
	verbose  bool

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

// ConnectOption is a functional option for Connect.
type ConnectOption func(*Desk)

// WithVerbose enables logging of raw BLE packets.
func WithVerbose(v bool) ConnectOption {
	return func(d *Desk) { d.verbose = v }
}

// Scan starts a BLE scan and calls cb for every device that advertises the
// FE60 service. It stops automatically after timeout.
func Scan(timeout time.Duration, cb func(addr bluetooth.Address, rssi int16, name string)) error {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable BLE adapter: %w", err)
	}
	done := make(chan struct{})
	time.AfterFunc(timeout, func() {
		adapter.StopScan()
		close(done)
	})
	adapter.Scan(func(a *bluetooth.Adapter, result bluetooth.ScanResult) { //nolint:errcheck
		if result.HasServiceUUID(serviceUUID) {
			cb(result.Address, result.RSSI, result.LocalName())
		}
	})
	<-done
	return nil
}

// Connect establishes a BLE connection to the desk at addr, discovers the
// required characteristics, and enables FE62 notifications.
//
// The notification setup is done in two phases intentionally:
//  1. Discover all characteristics (GATT discovery).
//  2. Wait for the connection to stabilise, subscribe to FE62 notifications,
//     then send a wake pulse to FE61.
//
// Subscribing BEFORE the first FE61 write is required: the Lierda BLE module
// (LSD4BT-E95ASTD001) rejects CCCD writes with ATT error 0x11 ("Insufficient
// Resources") if any command is sent to FE61 before the subscription.
func Connect(addrStr string, connectTimeout time.Duration, opts ...ConnectOption) (*Desk, error) {
	d := &Desk{}
	for _, o := range opts {
		o(d)
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return nil, fmt.Errorf("enable BLE adapter: %w", err)
	}

	d.Address.Set(addrStr)

	// adapter.Connect blocks; run it in a goroutine so we can apply a timeout.
	type connResult struct {
		dev bluetooth.Device
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		dev, err := adapter.Connect(d.Address, bluetooth.ConnectionParams{})
		ch <- connResult{dev, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("connect to %s: %w", addrStr, r.err)
		}
		d.device = r.dev
	case <-time.After(connectTimeout):
		return nil, fmt.Errorf("connect to %s: timed out after %s", addrStr, connectTimeout)
	}

	// ── Phase 1: GATT service & characteristic discovery ──────────────────────
	services, err := d.device.DiscoverServices([]bluetooth.UUID{
		serviceUUID,
		bluetooth.ServiceUUIDDeviceInformation,
	})
	if err != nil {
		return nil, fmt.Errorf("discover services: %w", err)
	}

	for _, svc := range services {
		switch svc.UUID() {
		case serviceUUID:
			if err := d.discoverDeskChars(svc); err != nil {
				return nil, err
			}
		case bluetooth.ServiceUUIDDeviceInformation:
			d.discoverInfoChars(svc) //nolint:errcheck — optional
		}
	}

	// ── Phase 2: subscribe to FE62 notifications, then wake the desk ────────────
	//
	// IMPORTANT: subscribe BEFORE sending any command to FE61.
	// Sending a wake (0x00) command before the CCCD write causes the desk's
	// Lierda BLE module to reject the subscription with ATT error 0x11
	// "Insufficient Resources".  Bleak (Python) succeeds because it subscribes
	// immediately after discovery, before writing any commands.
	time.Sleep(300 * time.Millisecond)

	// Disable any stale subscription first, then re-enable.
	d.respChar.EnableNotifications(nil) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	if err := d.respChar.EnableNotifications(func(buf []byte) {
		d.handleNotification(buf)
	}); err != nil {
		d.notifyErr = err
		d.verboseLogf("[BLE] FE62 notification setup failed (%v) — will still attempt height feedback", err)
	}

	// Wake the desk after subscribing so any height notifications the wake
	// triggers are captured by the now-registered handler.
	wakeCmd := protocol.MakeCommand(0x00)
	d.verboseLogf("[BLE tx] (wake post-subscribe) % X", wakeCmd)
	d.cmdChar.WriteWithoutResponse(wakeCmd) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	return d, nil
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
func (d *Desk) NotifyError() error {
	return d.notifyErr
}

// discoverDeskChars discovers FE61 / FE62 / FE63 characteristics and stores
// them.  Notification setup for FE62 is deferred to after this function
// returns — see Connect.
func (d *Desk) discoverDeskChars(svc bluetooth.DeviceService) error {
	chars, err := svc.DiscoverCharacteristics(nil)
	if err != nil {
		return fmt.Errorf("discover FE60 characteristics: %w", err)
	}

	foundCmd, foundResp := false, false
	for _, c := range chars {
		switch c.UUID() {
		case charCommand:
			d.cmdChar = c
			foundCmd = true
		case charResponse:
			d.respChar = c
			foundResp = true
		case charName:
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			d.Info.DeviceName = string(buf[:n])
		}
	}

	if !foundCmd {
		return fmt.Errorf("FE61 (command) characteristic not found")
	}
	if !foundResp {
		return fmt.Errorf("FE62 (response) characteristic not found")
	}
	return nil
}

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
// Returns an error only if the BLE Read itself fails.  Callers should treat
// that as a transport error, not as "height unavailable".
func (d *Desk) PollHeightDirect() error {
	buf := make([]byte, 32)
	n, err := d.respChar.Read(buf)
	if err != nil {
		return fmt.Errorf("read FE62: %w", err)
	}
	if n > 0 {
		d.handleNotification(buf[:n])
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

func (d *Desk) discoverInfoChars(svc bluetooth.DeviceService) error {
	chars, err := svc.DiscoverCharacteristics(nil)
	if err != nil {
		return nil
	}
	readStr := func(c bluetooth.DeviceCharacteristic) string {
		buf := make([]byte, 128)
		n, _ := c.Read(buf)
		return string(buf[:n])
	}
	for _, c := range chars {
		switch c.UUID() {
		case bluetooth.CharacteristicUUIDManufacturerNameString:
			d.Info.Manufacturer = readStr(c)
		case bluetooth.CharacteristicUUIDModelNumberString:
			d.Info.Model = readStr(c)
		case bluetooth.CharacteristicUUIDSerialNumberString:
			d.Info.Serial = readStr(c)
		case bluetooth.CharacteristicUUIDFirmwareRevisionString:
			d.Info.FirmwareRev = readStr(c)
		case bluetooth.CharacteristicUUIDHardwareRevisionString:
			d.Info.HardwareRev = readStr(c)
		case bluetooth.CharacteristicUUIDSoftwareRevisionString:
			d.Info.SoftwareRev = readStr(c)
		}
	}
	return nil
}

// DeviceInfo returns the desk's device-information fields.
// It is part of the [ipc.Controller] interface.
func (d *Desk) DeviceInfo() Info { return d.Info }

// DeviceAddress returns the desk's BLE address as a string.
// It is part of the [ipc.Controller] interface.
func (d *Desk) DeviceAddress() string { return d.Address.String() }

// Disconnect closes the BLE connection.
func (d *Desk) Disconnect() error {
	return d.device.Disconnect()
}
