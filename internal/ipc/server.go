package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/steigr/yaasa-go/desk"
)

// Controller is the set of operations the IPC server requires from a desk.
// *desk.Desk satisfies this interface; any alternative desk implementation
// (e.g. a simulated desk for testing) may implement it too.
type Controller interface {
	Wake() error
	MoveUp() error
	MoveDown() error
	Stop() error
	WaitForHeight(ctx context.Context, target desk.Height, tolerance desk.Height, timeout time.Duration, progress func(desk.Height)) error
	WaitForPreset(ctx context.Context, n int, timeout time.Duration, quiescence time.Duration, progress func(desk.Height)) error
	FetchSitStandTime(timeout time.Duration) (desk.SitStandTime, error)
	CurrentHeight(timeout time.Duration) (desk.Height, error)
	SavePreset(n int) error

	// DeviceInfo and DeviceAddress expose the read-only device metadata used
	// by the "info" and "status" commands without requiring direct struct-field
	// access.
	DeviceInfo() desk.Info
	DeviceAddress() string
	NotificationsAvailable() bool
}

// Server accepts connections and dispatches desk commands.
// Obtain one via [ListenWith] or the [UnixTransport] convenience wrapper [Listen].
type Server struct {
	listener   net.Listener
	controller Controller
	quitCh     chan struct{} // closed when a "quit" command is received
	once       sync.Once    // guards closing quitCh

	// Command preemption: only one dispatch runs at a time.
	// When a new command arrives it cancels the current one; the current
	// dispatch exits its select loop within one pulse interval (≤200 ms)
	// and releases dispatchMu, allowing the new command to proceed.
	dispatchMu    sync.Mutex         // held for the lifetime of one dispatch call
	cancelMu      sync.Mutex         // protects cancelCurrent
	cancelCurrent context.CancelFunc // cancels the active dispatch (nil initially)
}

// ListenWith starts the IPC server using the given transport and returns a
// ready [Server].  Returns an error if the transport cannot bind (e.g. the
// address is already in use).
func ListenWith(t Transport, c Controller) (*Server, error) {
	l, err := t.Listen()
	if err != nil {
		return nil, err
	}
	return &Server{
		listener:   l,
		controller: c,
		quitCh:     make(chan struct{}),
	}, nil
}

// Listen creates a Unix socket at socketPath and returns a ready [Server].
// Returns an error if another daemon is already running at that path.
func Listen(socketPath string, c Controller) (*Server, error) {
	return ListenWith(UnixTransport{Path: socketPath}, c)
}

// Close stops the server listener.
func (s *Server) Close() error { return s.listener.Close() }

// QuitCh returns a channel that is closed when a "quit" command is received.
func (s *Server) QuitCh() <-chan struct{} { return s.quitCh }

// Serve accepts connections in a loop until the listener is closed.
// Each connection handles exactly one request/response pair.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.quitCh:
				return nil // clean shutdown requested via "quit" command
			default:
				return err
			}
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	// Preempt any running operation and register this command's context.
	// The cancel+assign under cancelMu is atomic: whoever sets cancelCurrent
	// last wins, so the very latest command always runs.
	s.cancelMu.Lock()
	if s.cancelCurrent != nil {
		s.cancelCurrent()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelCurrent = cancel
	s.cancelMu.Unlock()
	defer cancel()

	// Wait for the previous dispatch to finish (it exits quickly once its
	// context is cancelled), then take exclusive ownership of the desk.
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()

	// If a still-newer command arrived while we were waiting for the lock,
	// our context was already cancelled — bail out so the newer one can run.
	if ctx.Err() != nil {
		_ = json.NewEncoder(conn).Encode(Response{OK: false, Error: "preempted by newer command"})
		return
	}

	resp := s.dispatch(ctx, req)
	_ = json.NewEncoder(conn).Encode(resp)

	// Trigger shutdown after the response is flushed.
	if req.Cmd == "quit" {
		s.once.Do(func() { close(s.quitCh) })
		_ = s.listener.Close()
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	c := s.controller
	switch req.Cmd {

	// ── motion ────────────────────────────────────────────────────────────────

	case "up", "down":
		dur := time.Duration(req.DurationMS) * time.Millisecond
		if dur <= 0 {
			dur = 500 * time.Millisecond
		}
		if err := c.Wake(); err != nil {
			return errR(err)
		}
		pulse := c.MoveUp
		if req.Cmd == "down" {
			pulse = c.MoveDown
		}
		// First pulse at t=0 — avoids the 200 ms ticker cold-start gap.
		if err := pulse(); err != nil {
			_ = c.Stop()
			return errR(err)
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(dur)
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-deadline:
				break loop
			case <-ticker.C:
				if err := pulse(); err != nil {
					_ = c.Stop()
					return errR(err)
				}
			}
		}
		_ = c.Stop()
		return okR()

	case "stop":
		if err := c.Stop(); err != nil {
			return errR(err)
		}
		return okR()

	case "move":
		tol := req.ToleranceMM
		if tol <= 0 {
			tol = 2.0
		}
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		target := desk.HeightFromMM(req.HeightMM)
		if err := c.WaitForHeight(ctx, target, desk.HeightFromMM(tol), timeout, nil); err != nil {
			return errR(err)
		}
		return okR()

	case "preset":
		if req.Save {
			if err := c.SavePreset(req.Preset); err != nil {
				return errR(err)
			}
			return okR()
		}
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if err := c.WaitForPreset(ctx, req.Preset, timeout, 0, nil); err != nil {
			return errR(err)
		}
		return okR()

	// ── read-only ─────────────────────────────────────────────────────────────

	case "stats":
		st, err := c.FetchSitStandTime(5 * time.Second)
		if err != nil {
			return errR(err)
		}
		return Response{
			OK:        true,
			StandSecs: int64(st.StandDuration().Seconds()),
			SitSecs:   int64(st.SitDuration().Seconds()),
		}

	case "height":
		h, err := c.CurrentHeight(5 * time.Second)
		if err != nil {
			return errR(err)
		}
		return Response{OK: true, HeightMM: h.MM()}

	case "info":
		i := c.DeviceInfo()
		return Response{
			OK:            true,
			Connected:     true,
			Notifications: c.NotificationsAvailable(),
			Address:       c.DeviceAddress(),
			DeviceName:    i.DeviceName,
			Model:         i.Model,
			Manufacturer:  i.Manufacturer,
			Serial:        i.Serial,
			FirmwareRev:   i.FirmwareRev,
			HardwareRev:   i.HardwareRev,
			SoftwareRev:   i.SoftwareRev,
		}

	case "status":
		return Response{
			OK:            true,
			Connected:     true,
			Notifications: c.NotificationsAvailable(),
			Address:       c.DeviceAddress(),
		}

	// ── lifecycle ─────────────────────────────────────────────────────────────

	case "quit":
		return okR() // actual shutdown happens in handle() after this returns

	default:
		return errR(fmt.Errorf("unknown command %q", req.Cmd))
	}
}

func okR() Response           { return Response{OK: true} }
func errR(err error) Response { return Response{OK: false, Error: err.Error()} }
