//go:build clib

// Package main is compiled exclusively when the "clib" build tag is set.
// It produces a C-compatible shared library (.dylib on macOS, .so on Linux)
// that exposes the desk package over a stable C ABI.
//
// Build:
//
//	# macOS
//	go build -buildmode=c-shared -tags clib -o libdeskcontrol.dylib ./clib/
//
//	# Linux
//	go build -buildmode=c-shared -tags clib -o libdeskcontrol.so ./clib/
//
// The compiler also emits a libdeskcontrol.h header with all exported
// declarations; include that file in your C/ObjC/Swift project.
//
// # Handle lifetime
//
// Every yaasa_connect() call allocates a Desk handle (uintptr_t).  Call
// yaasa_disconnect() when you are done; that closes the BLE connection AND
// releases the underlying Go handle so the GC can reclaim the Desk.
//
// # Thread safety
//
// All exported functions are safe to call from any thread.  Callbacks are
// dispatched on an internal goroutine; do not call back into the library
// from inside a callback.
//
// # Error strings
//
// Functions that can fail return a C string via an out-parameter (char**).
// On success the pointer is set to NULL.  On failure it points to a
// malloc'd, NUL-terminated string that the caller must free with
// yaasa_free_string().
package main

/*
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>

// Desk handle — opaque to C callers.
typedef uintptr_t yaasa_desk_t;

// Sit/stand time (seconds).
typedef struct {
    int64_t stand_secs;
    int64_t sit_secs;
} yaasa_stats_t;

// Callback types.
typedef void (*yaasa_scan_cb)(const char *addr, int16_t rssi, const char *name, void *user_data);
typedef void (*yaasa_height_cb)(double height_mm, void *user_data);
typedef void (*yaasa_stats_cb)(yaasa_stats_t stats, void *user_data);

// Trampolines — Go cannot store C function pointers directly, so these
// static helpers are called from Go via CGo.
static void call_scan_cb(yaasa_scan_cb cb, const char *addr, int16_t rssi,
                          const char *name, void *user_data) {
    if (cb) cb(addr, rssi, name, user_data);
}
static void call_height_cb(yaasa_height_cb cb, double height_mm, void *user_data) {
    if (cb) cb(height_mm, user_data);
}
static void call_stats_cb(yaasa_stats_cb cb, yaasa_stats_t stats, void *user_data) {
    if (cb) cb(stats, user_data);
}
*/
import "C"

import (
	"context"
	"fmt"
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/steigr/yaasa-go/desk"
	"tinygo.org/x/bluetooth"
)

func main() {} // required for -buildmode=c-shared

// ── cancel-handle registry ────────────────────────────────────────────────────
//
// AddHeightListener / AddSitStandListener return a Go cancel func.  We wrap
// each cancel func in a cgo.Handle so C callers can reference it as a uint64
// token and later pass that token to yaasa_cancel_listener().

var (
	cancelMu      sync.Mutex
	cancelHandles = map[uint64]func(){}
	cancelSeq     uint64
)

func registerCancel(fn func()) uint64 {
	id := atomic.AddUint64(&cancelSeq, 1)
	cancelMu.Lock()
	cancelHandles[id] = fn
	cancelMu.Unlock()
	return id
}

func invokeCancel(id uint64) {
	cancelMu.Lock()
	fn, ok := cancelHandles[id]
	delete(cancelHandles, id)
	cancelMu.Unlock()
	if ok {
		fn()
	}
}

// ── error helpers ─────────────────────────────────────────────────────────────

func setErr(errpp **C.char, err error) {
	if errpp == nil {
		return
	}
	if err == nil {
		*errpp = nil
	} else {
		*errpp = C.CString(err.Error())
	}
}

// ── exported C API ────────────────────────────────────────────────────────────

// yaasa_scan scans for desks advertising the FE60 service for timeout_ms
// milliseconds.  cb is called once per discovered device on an internal
// goroutine.  Returns 0 on success, -1 on error (details in *err_out).
//
//export yaasa_scan
func yaasa_scan(timeoutMS C.int64_t, cb C.yaasa_scan_cb, userData unsafe.Pointer, errOut **C.char) C.int {
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	err := desk.Scan(timeout, func(addr bluetooth.Address, rssi int16, name string) {
		cAddr := C.CString(addr.String())
		cName := C.CString(name)
		C.call_scan_cb(cb, cAddr, C.int16_t(rssi), cName, userData)
		C.free(unsafe.Pointer(cAddr))
		C.free(unsafe.Pointer(cName))
	})
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return 0
}

// yaasa_connect connects to the desk at addr (MAC on Linux, UUID on macOS).
// connect_timeout_ms sets the BLE connection timeout.
// Returns an opaque desk handle on success, or 0 on error.
//
//export yaasa_connect
func yaasa_connect(addr *C.char, connectTimeoutMS C.int64_t, errOut **C.char) C.yaasa_desk_t {
	timeout := time.Duration(connectTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	d, err := desk.Connect(C.GoString(addr), timeout)
	if err != nil {
		setErr(errOut, err)
		return 0
	}
	setErr(errOut, nil)
	h := cgo.NewHandle(d)
	return C.yaasa_desk_t(h)
}

// yaasa_disconnect closes the BLE connection and frees the desk handle.
//
//export yaasa_disconnect
func yaasa_disconnect(handle C.yaasa_desk_t) {
	if handle == 0 {
		return
	}
	h := cgo.Handle(handle)
	d := h.Value().(*desk.Desk)
	d.Disconnect() //nolint:errcheck
	h.Delete()
}

// yaasa_current_height_mm returns the current desk height in millimetres.
// timeout_ms is the maximum time to wait for a BLE notification.
// Returns -1 on error (details in *err_out).
//
//export yaasa_current_height_mm
func yaasa_current_height_mm(handle C.yaasa_desk_t, timeoutMS C.int64_t, errOut **C.char) C.double {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	h, err := d.CurrentHeight(timeout)
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return C.double(h.MM())
}

// yaasa_wait_for_height moves the desk to height_mm (within tolerance_mm) and
// waits for arrival.  timeout_ms is the overall deadline.
// Returns 0 on success, -1 on error.
//
//export yaasa_wait_for_height
func yaasa_wait_for_height(handle C.yaasa_desk_t,
	heightMM, toleranceMM C.double,
	timeoutMS C.int64_t,
	cb C.yaasa_height_cb, userData unsafe.Pointer,
	errOut **C.char) C.int {

	d := cgo.Handle(handle).Value().(*desk.Desk)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tol := float64(toleranceMM)
	if tol <= 0 {
		tol = 2.0
	}

	var progress func(desk.Height)
	if cb != nil {
		progress = func(h desk.Height) {
			C.call_height_cb(cb, C.double(h.MM()), userData)
		}
	}

	err := d.WaitForHeight(context.Background(),
		desk.HeightFromMM(float64(heightMM)),
		desk.HeightFromMM(tol),
		timeout, progress)
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return 0
}

// yaasa_wait_for_preset activates a preset (1–4) and waits for the desk to
// stop moving.  timeout_ms is the overall deadline; quiescence_ms is the
// silence-on-FE62 window that signals arrival (0 = default 500 ms).
// Returns 0 on success, -1 on error.
//
//export yaasa_wait_for_preset
func yaasa_wait_for_preset(handle C.yaasa_desk_t,
	preset C.int,
	timeoutMS, quiescenceMS C.int64_t,
	cb C.yaasa_height_cb, userData unsafe.Pointer,
	errOut **C.char) C.int {

	d := cgo.Handle(handle).Value().(*desk.Desk)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	quiescence := time.Duration(quiescenceMS) * time.Millisecond
	if quiescence <= 0 {
		quiescence = 500 * time.Millisecond
	}

	var progress func(desk.Height)
	if cb != nil {
		progress = func(h desk.Height) {
			C.call_height_cb(cb, C.double(h.MM()), userData)
		}
	}

	err := d.WaitForPreset(context.Background(), int(preset), timeout, quiescence, progress)
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return 0
}

// yaasa_move_up sends continuous move-up pulses for duration_ms milliseconds,
// then stops.  Returns 0 on success, -1 on error.
//
//export yaasa_move_up
func yaasa_move_up(handle C.yaasa_desk_t, durationMS C.int64_t, errOut **C.char) C.int {
	return movePulse(handle, durationMS, errOut, true)
}

// yaasa_move_down sends continuous move-down pulses for duration_ms
// milliseconds, then stops.  Returns 0 on success, -1 on error.
//
//export yaasa_move_down
func yaasa_move_down(handle C.yaasa_desk_t, durationMS C.int64_t, errOut **C.char) C.int {
	return movePulse(handle, durationMS, errOut, false)
}

func movePulse(handle C.yaasa_desk_t, durationMS C.int64_t, errOut **C.char, up bool) C.int {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	dur := time.Duration(durationMS) * time.Millisecond
	if dur <= 0 {
		dur = 500 * time.Millisecond
	}
	pulse := d.MoveUp
	if !up {
		pulse = d.MoveDown
	}
	if err := d.Wake(); err != nil {
		setErr(errOut, fmt.Errorf("wake: %w", err))
		return -1
	}
	if err := pulse(); err != nil {
		d.Stop() //nolint:errcheck
		setErr(errOut, err)
		return -1
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(dur)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			if err := pulse(); err != nil {
				d.Stop() //nolint:errcheck
				setErr(errOut, err)
				return -1
			}
		}
	}
	if err := d.Stop(); err != nil {
		setErr(errOut, err)
		return -1
	}
	setErr(errOut, nil)
	return 0
}

// yaasa_stop sends a stop command immediately.
// Returns 0 on success, -1 on error.
//
//export yaasa_stop
func yaasa_stop(handle C.yaasa_desk_t, errOut **C.char) C.int {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	err := d.Stop()
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return 0
}

// yaasa_wake sends a wake command.
// Returns 0 on success, -1 on error.
//
//export yaasa_wake
func yaasa_wake(handle C.yaasa_desk_t, errOut **C.char) C.int {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	err := d.Wake()
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	return 0
}

// yaasa_fetch_stats fetches the current sit/stand time counters.
// timeout_ms is the maximum time to wait for the desk's response.
// Returns 0 on success, -1 on error.
//
//export yaasa_fetch_stats
func yaasa_fetch_stats(handle C.yaasa_desk_t, timeoutMS C.int64_t, out *C.yaasa_stats_t, errOut **C.char) C.int {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	s, err := d.FetchSitStandTime(timeout)
	setErr(errOut, err)
	if err != nil {
		return -1
	}
	out.stand_secs = C.int64_t(s.StandDuration().Seconds())
	out.sit_secs = C.int64_t(s.SitDuration().Seconds())
	return 0
}

// yaasa_add_height_listener registers a callback that fires on every height
// notification.  Returns a listener ID that can be passed to
// yaasa_cancel_listener() to unregister.
//
//export yaasa_add_height_listener
func yaasa_add_height_listener(handle C.yaasa_desk_t, cb C.yaasa_height_cb, userData unsafe.Pointer) C.uint64_t {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	cancelFn := d.AddHeightListener(func(h desk.Height) {
		C.call_height_cb(cb, C.double(h.MM()), userData)
	})
	return C.uint64_t(registerCancel(cancelFn))
}

// yaasa_add_stats_listener registers a callback that fires on every 0xA2
// sit/stand time notification.  Returns a listener ID.
//
//export yaasa_add_stats_listener
func yaasa_add_stats_listener(handle C.yaasa_desk_t, cb C.yaasa_stats_cb, userData unsafe.Pointer) C.uint64_t {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	cancelFn := d.AddSitStandListener(func(s desk.SitStandTime) {
		var cs C.yaasa_stats_t
		cs.stand_secs = C.int64_t(s.StandDuration().Seconds())
		cs.sit_secs = C.int64_t(s.SitDuration().Seconds())
		C.call_stats_cb(cb, cs, userData)
	})
	return C.uint64_t(registerCancel(cancelFn))
}

// yaasa_cancel_listener unregisters a listener previously returned by
// yaasa_add_height_listener or yaasa_add_stats_listener.
//
//export yaasa_cancel_listener
func yaasa_cancel_listener(listenerID C.uint64_t) {
	invokeCancel(uint64(listenerID))
}

// yaasa_device_name returns the desk's BLE device name (FE63 characteristic).
// The returned string is owned by the caller and must be freed with
// yaasa_free_string().
//
//export yaasa_device_name
func yaasa_device_name(handle C.yaasa_desk_t) *C.char {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	return C.CString(d.Info.DeviceName)
}

// yaasa_device_model returns the model string from the Device Information
// Service (0x180A).  Caller must free with yaasa_free_string().
//
//export yaasa_device_model
func yaasa_device_model(handle C.yaasa_desk_t) *C.char {
	d := cgo.Handle(handle).Value().(*desk.Desk)
	return C.CString(d.Info.Model)
}

// yaasa_free_string frees a string returned by the library.
//
//export yaasa_free_string
func yaasa_free_string(s *C.char) {
	C.free(unsafe.Pointer(s))
}
