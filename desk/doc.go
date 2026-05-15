// Package desk provides a high-level BLE API for Jiecang FE60 standing desks.
//
// Tested hardware: Jiecang FE60 controller with Lierda LSD4BT-E95ASTD001 BLE
// module, sold as the Yaasa Frame Expert and Uplift Desk (among others).
// The FE60 GATT service is advertised at 16-bit UUID 0xFE60; commands are
// written to characteristic 0xFE61 and height/stat responses arrive as
// notifications on 0xFE62.
//
// # Getting started
//
// Scan for nearby desks, then connect by address:
//
//	err := desk.Scan(10*time.Second, func(addr bluetooth.Address, rssi int16, name string) {
//		fmt.Println(addr, rssi, name)
//	})
//
//	d, err := desk.Connect(addr.String(), 15*time.Second)
//
// Connect performs two-phase setup: it discovers GATT characteristics, waits
// briefly for the BLE connection to stabilise, subscribes to FE62
// notifications, then sends a wake pulse.  This order is required — see
// "Known firmware limitations" below.
//
// # Reading height
//
// Fast path (cache warmed by any movement):
//
//	h, ok := d.LastKnownHeight()
//
// Slow path (blocks until a notification arrives or timeout):
//
//	h, err := d.CurrentHeight(5 * time.Second)
//
// The height cache is refreshed by every FE62 opcode-0x01 notification.
// Any command that causes desk movement — up, down, preset, move-to — also
// triggers a burst of notifications that warms the cache.  On a completely
// idle cold desk CurrentHeight may time out; run any motion command first.
//
// # Moving the desk
//
// Move to an absolute height and wait for arrival (±2 mm tolerance, 30 s max):
//
//	err := d.WaitForHeight(ctx, desk.HeightFromMM(720), desk.HeightFromMM(2), 30*time.Second, func(h desk.Height) {
//		fmt.Printf("  → %s\r", h)
//	})
//
// Move to a stored preset and wait for the desk to stop moving:
//
//	err := d.WaitForPreset(ctx, 1, 30*time.Second, 500*time.Millisecond, nil)
//
// Duration-based movement (no height feedback needed):
//
//	d.Wake()
//	d.MoveUp()
//	time.Sleep(2 * time.Second)
//	d.Stop()
//
// # Sit/stand statistics
//
// Fetch the current accumulated counters (real-time on the desk):
//
//	s, err := d.FetchSitStandTime(5 * time.Second)
//	fmt.Printf("stand %s  sit %s\n", s.StandDuration(), s.SitDuration())
//
// # Known firmware limitations (Yaasa OEM firmware)
//
// These limitations apply to the Yaasa Frame Expert firmware; Uplift hardware
// may behave differently.
//
//   - Height notifications only arrive during movement.  CurrentHeight times out
//     on a completely idle cold desk because the FE60 firmware does not respond
//     to an explicit height-query command with a real-time value at rest.
//
//   - A single MoveUp or MoveDown pulse causes approximately 17 mm of travel due
//     to motor inertia.  Do NOT use a single pulse as a height probe or for
//     fine-grained positioning; use WaitForHeight instead.
//
//   - FE62 notifications MUST be subscribed to BEFORE any FE61 write.  Sending a
//     wake command before the CCCD subscription causes the Lierda BLE module to
//     reject the subscription with ATT error 0x11 ("Insufficient Resources").
//     Connect handles this ordering automatically.
//
//   - Opcodes 0x0E (units), 0x19 (touch mode), 0x0C (height range), 0x25–0x28
//     (preset heights) do NOT respond on Yaasa firmware.  They work on Uplift
//     hardware only.
//
//   - Opcode 0xA2 (sit/stand time) and 0xAA (all-time stats) DO respond on
//     Yaasa firmware.  0xAA does not update in real time — use 0xA2 for live
//     counters.
package desk
