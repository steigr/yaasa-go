package desk

import (
	"fmt"
	"time"

	"tinygo.org/x/bluetooth"
)

var (
	serviceUUID  = bluetooth.New16BitUUID(0xFE60)
	charCommand  = bluetooth.New16BitUUID(0xFE61) // write-only
	charResponse = bluetooth.New16BitUUID(0xFE62) // notify
	charName     = bluetooth.New16BitUUID(0xFE63) // read — device name string
)

// TinygoAdapter implements [Adapter] using [tinygo.org/x/bluetooth].
//
// It uses bluetooth.DefaultAdapter, which maps to CoreBluetooth on macOS and
// BlueZ on Linux.  No configuration is required; use a zero value:
//
//	d, err := desk.ConnectWith(desk.TinygoAdapter{}, addr, 15*time.Second)
type TinygoAdapter struct{}

// Scan starts a BLE scan and calls cb for every device advertising the FE60
// service (16-bit UUID 0xFE60).  It stops after timeout elapses.
func (TinygoAdapter) Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error {
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
			cb(result.Address.String(), result.LocalName(), result.RSSI)
		}
	})
	<-done
	return nil
}

// Connect establishes a BLE connection to the desk at addr, performs two-phase
// GATT setup, and returns a ready [Connection].
//
// Phase 1: discover the FE60 service (FE61/FE62/FE63 characteristics) and the
// Device Information service.
//
// Phase 2: wait 300 ms for the connection to stabilise — the Lierda
// LSD4BT-E95ASTD001 BLE module rejects CCCD writes with ATT error 0x11
// ("Insufficient Resources") if a command is sent to FE61 before the CCCD
// subscription, so the stabilisation delay is mandatory before the caller
// invokes [Connection.EnableNotifications].
func (TinygoAdapter) Connect(addr string, timeout time.Duration) (Connection, Info, error) {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return nil, Info{}, fmt.Errorf("enable BLE adapter: %w", err)
	}

	var bleAddr bluetooth.Address
	bleAddr.Set(addr)

	type connResult struct {
		dev bluetooth.Device
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		dev, err := adapter.Connect(bleAddr, bluetooth.ConnectionParams{})
		ch <- connResult{dev, err}
	}()

	var dev bluetooth.Device
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, Info{}, fmt.Errorf("connect to %s: %w", addr, r.err)
		}
		dev = r.dev
	case <-time.After(timeout):
		return nil, Info{}, fmt.Errorf("connect to %s: timed out after %s", addr, timeout)
	}

	// ── Phase 1: GATT service & characteristic discovery ─────────────────────
	services, err := dev.DiscoverServices([]bluetooth.UUID{
		serviceUUID,
		bluetooth.ServiceUUIDDeviceInformation,
	})
	if err != nil {
		return nil, Info{}, fmt.Errorf("discover services: %w", err)
	}

	tc := &tinygoConnection{device: dev}
	var info Info

	for _, svc := range services {
		switch svc.UUID() {
		case serviceUUID:
			if err := tc.discoverDeskChars(svc); err != nil {
				return nil, Info{}, err
			}
		case bluetooth.ServiceUUIDDeviceInformation:
			tc.discoverInfoChars(svc, &info) //nolint:errcheck — optional
		}
	}

	// ── Phase 2: stabilisation delay ─────────────────────────────────────────
	//
	// The Lierda module (LSD4BT-E95ASTD001) rejects CCCD writes if any FE61
	// command is sent before the CCCD subscription.  Wait for the connection
	// to stabilise before the caller enables notifications.
	time.Sleep(300 * time.Millisecond)

	return tc, info, nil
}

// tinygoConnection is the concrete [Connection] produced by [TinygoAdapter].
// It holds live GATT characteristic handles for FE61 and FE62.
type tinygoConnection struct {
	device   bluetooth.Device
	cmdChar  bluetooth.DeviceCharacteristic // FE61 — write-only
	respChar bluetooth.DeviceCharacteristic // FE62 — notify
}

// WriteCommand sends data to FE61 (write-without-response).
func (c *tinygoConnection) WriteCommand(data []byte) error {
	_, err := c.cmdChar.WriteWithoutResponse(data)
	return err
}

// EnableNotifications registers cb as the FE62 notification handler.
//
// Any stale CCCD subscription is disabled first (with a 100 ms gap) to work
// around the Lierda module's "Insufficient Resources" (ATT 0x11) behaviour on
// reconnect.  This delay is in addition to the stabilisation sleep in
// [TinygoAdapter.Connect].
func (c *tinygoConnection) EnableNotifications(cb func([]byte)) error {
	// Disable stale subscription before re-enabling.
	c.respChar.EnableNotifications(nil) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)
	return c.respChar.EnableNotifications(cb)
}

// ReadResponse reads the current FE62 characteristic value via a GATT Read
// (no CCCD required).  Used as a fallback when notification setup fails.
func (c *tinygoConnection) ReadResponse() ([]byte, error) {
	buf := make([]byte, 32)
	n, err := c.respChar.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read FE62: %w", err)
	}
	return buf[:n], nil
}

// Disconnect closes the underlying BLE connection.
func (c *tinygoConnection) Disconnect() error {
	return c.device.Disconnect()
}

// discoverDeskChars discovers FE61 / FE62 / FE63 and stores the handles.
func (c *tinygoConnection) discoverDeskChars(svc bluetooth.DeviceService) error {
	chars, err := svc.DiscoverCharacteristics(nil)
	if err != nil {
		return fmt.Errorf("discover FE60 characteristics: %w", err)
	}
	foundCmd, foundResp := false, false
	for _, ch := range chars {
		switch ch.UUID() {
		case charCommand:
			c.cmdChar = ch
			foundCmd = true
		case charResponse:
			c.respChar = ch
			foundResp = true
		case charName:
			// FE63 is read at discovery time so it is available in Info.
			// (stored by the caller via discoverInfoChars)
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

// discoverInfoChars reads Device Information Service characteristics and the
// FE63 device name into info.  Errors are ignored — the service is optional.
func (c *tinygoConnection) discoverInfoChars(svc bluetooth.DeviceService, info *Info) {
	chars, err := svc.DiscoverCharacteristics(nil)
	if err != nil {
		return
	}
	readStr := func(ch bluetooth.DeviceCharacteristic) string {
		buf := make([]byte, 128)
		n, _ := ch.Read(buf)
		return string(buf[:n])
	}
	for _, ch := range chars {
		switch ch.UUID() {
		case bluetooth.CharacteristicUUIDManufacturerNameString:
			info.Manufacturer = readStr(ch)
		case bluetooth.CharacteristicUUIDModelNumberString:
			info.Model = readStr(ch)
		case bluetooth.CharacteristicUUIDSerialNumberString:
			info.Serial = readStr(ch)
		case bluetooth.CharacteristicUUIDFirmwareRevisionString:
			info.FirmwareRev = readStr(ch)
		case bluetooth.CharacteristicUUIDHardwareRevisionString:
			info.HardwareRev = readStr(ch)
		case bluetooth.CharacteristicUUIDSoftwareRevisionString:
			info.SoftwareRev = readStr(ch)
		}
	}
	// FE63 — device name (part of the FE60 service, discovered separately
	// in discoverDeskChars; read it here if present).
	for _, ch := range chars {
		if ch.UUID() == charName {
			info.DeviceName = readStr(ch)
		}
	}
}
