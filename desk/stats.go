package desk

import (
	"fmt"
	"time"
)

// SitStandTime holds the desk's accumulated sit/stand time counters.
//
// The format was confirmed by empirical testing on a Yaasa Frame Expert
// (Jiecang FE60 / Lierda LSD4BT-E95ASTD001): a 48-second sit session caused
// the sit counter to increment by exactly 48 seconds, confirming real-time
// counting in plain binary (not BCD).
//
// Wire format: opcode 0xA2 FE62 notification, 6-byte payload:
//
//	[standH standM standS sitH sitM sitS]
//
// Each byte is a plain binary integer (HH:MM:SS), not BCD-encoded.
type SitStandTime struct {
	StandH, StandM, StandS uint8
	SitH, SitM, SitS       uint8
}

// StandDuration returns the accumulated standing time.
func (t SitStandTime) StandDuration() time.Duration {
	return time.Duration(t.StandH)*time.Hour +
		time.Duration(t.StandM)*time.Minute +
		time.Duration(t.StandS)*time.Second
}

// SitDuration returns the accumulated sitting time.
func (t SitStandTime) SitDuration() time.Duration {
	return time.Duration(t.SitH)*time.Hour +
		time.Duration(t.SitM)*time.Minute +
		time.Duration(t.SitS)*time.Second
}

// String returns a human-readable summary.
func (t SitStandTime) String() string {
	return fmt.Sprintf("stand %s  sit %s",
		formatHMS(t.StandDuration()), formatHMS(t.SitDuration()))
}

func formatHMS(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
}

// decodeSitStandTime parses a 6-byte 0xA2 FE62 notification payload.
func decodeSitStandTime(opcode byte, payload []byte) (SitStandTime, bool) {
	if opcode != 0xA2 || len(payload) < 6 {
		return SitStandTime{}, false
	}
	return SitStandTime{
		StandH: payload[0], StandM: payload[1], StandS: payload[2],
		SitH:   payload[3], SitM:   payload[4], SitS:   payload[5],
	}, true
}

// AddSitStandListener registers cb for every 0xA2 notification.
// Returns a cancel function that unregisters the listener.
func (d *Desk) AddSitStandListener(cb func(SitStandTime)) (cancel func()) {
	d.statsListenersMu.Lock()
	d.statsListeners = append(d.statsListeners, cb)
	idx := len(d.statsListeners) - 1
	d.statsListenersMu.Unlock()
	return func() {
		d.statsListenersMu.Lock()
		defer d.statsListenersMu.Unlock()
		if idx < len(d.statsListeners) {
			d.statsListeners = append(d.statsListeners[:idx], d.statsListeners[idx+1:]...)
		}
	}
}

// LastKnownSitStandTime returns the most recently received stats notification
// and true, or (zero, false) if no 0xA2 notification has arrived since connect.
func (d *Desk) LastKnownSitStandTime() (SitStandTime, bool) {
	d.lastStatsMu.RLock()
	defer d.lastStatsMu.RUnlock()
	return d.lastStats, d.hasStats
}

// FetchSitStandTime sends a 0xA2 request to FE61 and waits for the matching
// FE62 notification.  Always sends a fresh request so the returned value
// reflects the current second (the desk updates its counters in real time).
//
// The minimum effective timeout is 5 s to accommodate slow desk responses.
func (d *Desk) FetchSitStandTime(timeout time.Duration) (SitStandTime, error) {
	ch := make(chan SitStandTime, 1)
	cancel := d.AddSitStandListener(func(s SitStandTime) {
		select {
		case ch <- s:
		default:
		}
	})
	defer cancel()

	if err := d.RequestSitStandTime(); err != nil {
		return SitStandTime{}, fmt.Errorf("request sit/stand time: %w", err)
	}

	wait := timeout
	if wait < 5*time.Second {
		wait = 5 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	select {
	case s := <-ch:
		return s, nil
	case <-deadline.C:
		return SitStandTime{}, fmt.Errorf("no sit/stand time response from desk (timeout %s)", wait)
	}
}
