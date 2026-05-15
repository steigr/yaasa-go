package desk

import "time"

// Adapter abstracts the Bluetooth stack used to scan for and connect to
// standing desks.
//
// The bundled [TinygoAdapter] is the reference implementation; it uses
// [tinygo.org/x/bluetooth] and works on macOS (CoreBluetooth) and Linux
// (BlueZ).  Pass a custom [Adapter] to [ConnectWith] / [ScanWith] to use a
// different Bluetooth stack — for example a mock for tests or a platform SDK
// not supported by tinygo.
//
// # Implementing a custom Adapter
//
// Implement both interfaces and pass the adapter to [ScanWith] / [ConnectWith]:
//
//	type MyAdapter struct{ /* platform handles */ }
//
//	func (a MyAdapter) Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error {
//	    // Initiate a BLE scan; call cb for every FE60 advertisement found.
//	    // Return when timeout elapses or when scanning is complete.
//	    ...
//	}
//
//	func (a MyAdapter) Connect(addr string, timeout time.Duration) (desk.Connection, desk.Info, error) {
//	    // Establish a GATT connection, discover the FE60 service
//	    // (characteristics FE61 / FE62 / FE63) and the Device Information
//	    // service.  Return a Connection that wraps the live GATT handles plus
//	    // the device metadata read during discovery.
//	    //
//	    // The caller (ConnectWith) will invoke Connection.EnableNotifications
//	    // immediately after Connect returns, so the GATT connection must be
//	    // ready to accept a CCCD write.  Any adapter-side stabilisation delay
//	    // (e.g. the 300 ms wait required by the Lierda LSD4BT-E95ASTD001 module)
//	    // must be applied inside Connect before returning.
//	    ...
//	}
//
//	// Scan desks with the custom adapter:
//	err := desk.ScanWith(MyAdapter{}, 10*time.Second, func(addr, name string, rssi int16) {
//	    fmt.Println(addr, rssi, name)
//	})
//
//	// Connect with the custom adapter:
//	d, err := desk.ConnectWith(MyAdapter{}, "AA:BB:CC:DD:EE:FF", 15*time.Second)
type Adapter interface {
	// Scan initiates a BLE scan for desks advertising the FE60 service and
	// calls cb for each discovered device.  It must return after timeout
	// elapses.  If the adapter cannot start scanning it must return an error.
	Scan(timeout time.Duration, cb func(addr, name string, rssi int16)) error

	// Connect establishes a GATT connection to the desk at addr, discovers
	// the required characteristics, reads device metadata, and returns a
	// ready [Connection].  It must return an error if the connection cannot
	// be established within timeout.
	//
	// The returned [Info] contains device metadata (model, serial, etc.) read
	// from the Device Information service during GATT discovery.
	//
	// The caller will call [Connection.EnableNotifications] immediately after
	// Connect returns.  Any adapter-side stabilisation delay must be applied
	// inside Connect before it returns.
	Connect(addr string, timeout time.Duration) (Connection, Info, error)
}

// Connection represents a live GATT connection to one desk.
//
// All methods must be safe for concurrent use from multiple goroutines.
//
// # Implementing a custom Connection
//
// Implement all four methods to satisfy the interface.  The concrete tinygo
// implementation ([TinygoAdapter]) is a useful reference.  Key invariants:
//
//   - [Connection.WriteCommand] must send a write-without-response to the
//     FE61 characteristic; latency matters — the desk firmware expects pulses
//     every 200 ms during movement.
//
//   - [Connection.EnableNotifications] must register cb so that every FE62
//     attribute-value notification is delivered to cb.  If the stack requires
//     disabling a stale CCCD subscription before re-enabling (as the Lierda
//     module does), do so inside this method.
//
//   - [Connection.ReadResponse] must perform a synchronous GATT Read on FE62.
//     It is called as a fallback when notifications are unavailable.
//
//   - [Connection.Disconnect] must close the underlying GATT connection.
type Connection interface {
	// WriteCommand sends a raw command packet to the desk (FE61
	// write-without-response).  data is a complete framed packet produced by
	// [github.com/steigr/yaasa-go/internal/protocol.MakeCommand].
	WriteCommand(data []byte) error

	// EnableNotifications registers cb to be called for every FE62 attribute
	// value notification.  Passing a non-nil cb enables notifications;
	// implementations may disable any previous subscription before enabling
	// the new one.
	EnableNotifications(cb func([]byte)) error

	// ReadResponse reads the current FE62 characteristic value directly
	// (GATT Read, no CCCD required).  Used as a fallback when notification
	// setup fails.
	ReadResponse() ([]byte, error)

	// Disconnect closes the GATT connection.
	Disconnect() error
}
