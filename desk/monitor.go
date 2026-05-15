package desk

import (
	"context"
	"fmt"
	"time"
)

const (
	moveCommandPulseInterval = 200 * time.Millisecond
)

// CurrentHeight returns the current desk height.
//
// Fast path: if any FE62 opcode-0x01 height notification has been received
// since Connect, the cached value is returned immediately — no movement or
// BLE round-trip is needed.  The cache is refreshed by every height
// notification; any movement command (up/down/preset/move) triggers a burst
// that warms it.
//
// Slow path (cold start only): no height has been seen yet.  We send a
// RequestHeightLimits (opcode 0x07), which occasionally triggers a delayed
// (~4 s) height notification as a side effect.  If no notification arrives
// within the timeout (minimum 5 s), an error is returned describing the
// situation.  Running any movement command first guarantees the fast path on
// the next call.
func (d *Desk) CurrentHeight(timeout time.Duration) (Height, error) {
	// Fast path — return cached height if the daemon has seen any notification.
	if h, ok := d.LastKnownHeight(); ok {
		return h, nil
	}

	// Slow path (cold start) — the desk only streams FE62 height notifications
	// during movement.  A single MoveUp/Down pulse causes ~17 mm of travel due
	// to motor inertia, which is not acceptable.
	//
	// The only movement-free option observed is opcode 0x07 (height-limits
	// request), which occasionally triggers a delayed (~4 s) 0x01 notification
	// as a side effect.  We send it and wait; if the desk responds, great.
	// If it times out the caller gets an actionable error — run any movement
	// command first to warm the cache.
	ch := make(chan Height, 1)
	cancel := d.AddHeightListener(func(h Height) {
		select {
		case ch <- h:
		default:
		}
	})
	defer cancel()

	if err := d.RequestHeightLimits(); err != nil {
		return 0, fmt.Errorf("request height: %w", err)
	}

	// Allow at least 5 s; the observed desk response latency is ~4 s.
	wait := timeout
	if wait < 5*time.Second {
		wait = 5 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	select {
	case h := <-ch:
		return h, nil
	case <-deadline.C:
		return 0, fmt.Errorf(
			"no height data yet — the desk only reports height during movement.\n" +
				"Run any movement command first (up/down/preset/move) to warm the cache,\n" +
				"then height will return instantly.",
		)
	}
}

// WaitForHeight drives the desk to target and blocks until it arrives within
// tolerance, or until timeout elapses.
//
// The desk firmware requires repeated opcode-0x1B pulses every 200 ms to keep
// moving; this function handles the pulsing automatically using a ticker.
// Height notifications arrive as a side effect of the movement and are used to
// detect arrival — no separate polling is needed.
//
// progress is called on every incoming height notification (may be nil).
func (d *Desk) WaitForHeight(
	ctx context.Context,
	target Height,
	tolerance Height,
	timeout time.Duration,
	progress func(current Height),
) error {
	arrived := make(chan struct{}, 1)

	cancel := d.AddHeightListener(func(h Height) {
		if progress != nil {
			progress(h)
		}
		if h.Near(target, tolerance) {
			select {
			case arrived <- struct{}{}:
			default:
			}
		}
	})
	defer cancel()

	if err := d.Wake(); err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	// First 0x1B immediately — avoids the 200 ms ticker cold-start gap.
	if err := d.MoveToHeight(target); err != nil {
		return fmt.Errorf("move to height: %w", err)
	}

	ticker := time.NewTicker(moveCommandPulseInterval)
	defer ticker.Stop()
	deadline := time.After(timeout)

	for {
		select {
		case <-ctx.Done():
			d.Stop() //nolint:errcheck
			return fmt.Errorf("move cancelled")
		case <-arrived:
			d.Stop() //nolint:errcheck
			return nil
		case <-deadline:
			d.Stop() //nolint:errcheck
			return d.heightTimeoutError(fmt.Sprintf("desk did not reach %s", target), timeout, nil)
		case <-ticker.C:
			if err := d.MoveToHeight(target); err != nil {
				d.Stop() //nolint:errcheck
				return fmt.Errorf("move to height: %w", err)
			}
		}
	}
}

// WaitForPreset drives the desk to stored preset n and blocks until movement
// stops, or until timeout elapses.
//
// Completion is detected via quiescence: the desk streams FE62 height
// notifications while moving; when no notification arrives for quiescence the
// desk is considered stopped.  If quiescence is zero or negative, 500 ms is
// used.
//
// Like WaitForHeight, the preset command is re-sent every 200 ms because the
// firmware treats it as a continuous direction command.
//
// progress is called on every incoming height notification (may be nil).
func (d *Desk) WaitForPreset(
	ctx context.Context,
	n int,
	timeout time.Duration,
	quiescence time.Duration,
	progress func(current Height),
) error {
	if quiescence <= 0 {
		quiescence = 500 * time.Millisecond
	}

	heightCh := make(chan Height, 4)
	cancel := d.AddHeightListener(func(h Height) {
		if progress != nil {
			progress(h)
		}
		select {
		case heightCh <- h:
		default:
		}
	})
	defer cancel()

	if err := d.Wake(); err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	if err := d.GoPreset(n); err != nil {
		return fmt.Errorf("go preset: %w", err)
	}

	ticker := time.NewTicker(moveCommandPulseInterval)
	defer ticker.Stop()
	deadline := time.After(timeout)

	// quiescenceTimer is armed only after the first height notification so
	// that a slow desk start-up does not trigger a false early exit.
	var quiescenceTimer *time.Timer
	var quiescenceC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			d.Stop() //nolint:errcheck
			return fmt.Errorf("preset cancelled")
		case <-heightCh:
			// Movement confirmed — (re)start the quiescence timer.
			if quiescenceTimer == nil {
				quiescenceTimer = time.NewTimer(quiescence)
				defer quiescenceTimer.Stop()
				quiescenceC = quiescenceTimer.C
			} else {
				if !quiescenceTimer.Stop() {
					select {
					case <-quiescenceTimer.C:
					default:
					}
				}
				quiescenceTimer.Reset(quiescence)
			}
		case <-quiescenceC:
			// No notification for quiescence period — desk has stopped.
			d.Stop() //nolint:errcheck
			return nil
		case <-deadline:
			d.Stop() //nolint:errcheck
			return d.heightTimeoutError(fmt.Sprintf("preset %d did not complete", n), timeout, nil)
		case <-ticker.C:
			if err := d.GoPreset(n); err != nil {
				d.Stop() //nolint:errcheck
				return fmt.Errorf("go preset: %w", err)
			}
		}
	}
}

func (d *Desk) heightTimeoutError(prefix string, timeout time.Duration, lastPollErr error) error {
	hint := ""
	if d.notifyErr != nil {
		hint += fmt.Sprintf("\n\n(FE62 notification setup returned an error: %v)", d.notifyErr)
	}
	if lastPollErr != nil {
		hint += fmt.Sprintf("\nLast poll error: %v", lastPollErr)
	}
	if hint != "" {
		hint += "\nTry power-cycling the desk or removing it from macOS Bluetooth preferences\n(System Settings → Bluetooth → forget the device) then reconnect."
	}
	return fmt.Errorf("%s after %s%s", prefix, timeout, hint)
}
