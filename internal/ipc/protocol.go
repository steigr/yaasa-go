// Package ipc implements the request/response protocol between the yaasa
// daemon and CLI clients.
//
// Every interaction is a single request/response pair; both messages are
// newline-terminated JSON objects.  The underlying connection is provided by a
// [Transport]; the bundled reference implementation uses a Unix domain socket
// ([UnixTransport]).
package ipc

// Request is sent by a CLI client to the daemon.
type Request struct {
	Cmd string `json:"cmd"`

	// up / down
	DurationMS int `json:"duration_ms,omitempty"`

	// move
	HeightMM    float64 `json:"height_mm,omitempty"`
	ToleranceMM float64 `json:"tolerance_mm,omitempty"`
	TimeoutMS   int64   `json:"timeout_ms,omitempty"`

	// preset
	Preset int  `json:"preset,omitempty"`
	Save   bool `json:"save,omitempty"`
}

// Response is sent back by the daemon.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// height / move
	HeightMM float64 `json:"height_mm,omitempty"`

	// stats
	StandSecs int64 `json:"stand_secs,omitempty"`
	SitSecs   int64 `json:"sit_secs,omitempty"`

	// info / status
	Connected     bool   `json:"connected,omitempty"`
	Notifications bool   `json:"notifications,omitempty"`
	Address       string `json:"address,omitempty"`
	DeviceName    string `json:"device_name,omitempty"`
	Model         string `json:"model,omitempty"`
	Manufacturer  string `json:"manufacturer,omitempty"`
	Serial        string `json:"serial,omitempty"`
	FirmwareRev   string `json:"firmware_rev,omitempty"`
	HardwareRev   string `json:"hardware_rev,omitempty"`
	SoftwareRev   string `json:"software_rev,omitempty"`
}
