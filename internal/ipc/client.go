package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is a short-lived connection to the yaasa daemon.
type Client struct {
	conn net.Conn
}

// DialWith connects to the daemon using the provided transport.
// Returns an error immediately if the daemon is not running.
func DialWith(t Transport) (*Client, error) {
	conn, err := t.Dial()
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Dial connects to the daemon Unix socket at socketPath.
// Returns an error immediately if the daemon is not running.
func Dial(socketPath string) (*Client, error) {
	return DialWith(UnixTransport{Path: socketPath})
}

// Close shuts down the client connection.
func (c *Client) Close() error { return c.conn.Close() }

// Do sends req and decodes the response.  Returns an error if the transport
// fails OR if the daemon reports ok=false.
func (c *Client) Do(req Request) (*Response, error) {
	deadline := 60 * time.Second
	if req.TimeoutMS > 0 && time.Duration(req.TimeoutMS)*time.Millisecond+5*time.Second > deadline {
		deadline = time.Duration(req.TimeoutMS)*time.Millisecond + 5*time.Second
	}
	if req.DurationMS > 0 && time.Duration(req.DurationMS)*time.Millisecond+5*time.Second > deadline {
		deadline = time.Duration(req.DurationMS)*time.Millisecond + 5*time.Second
	}
	c.conn.SetDeadline(time.Now().Add(deadline)) //nolint:errcheck

	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send to daemon: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(c.conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("recv from daemon: %w", err)
	}
	if !resp.OK {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// ── convenience wrappers ──────────────────────────────────────────────────────

func (c *Client) Up(ms int) (*Response, error) {
	return c.Do(Request{Cmd: "up", DurationMS: ms})
}
func (c *Client) Down(ms int) (*Response, error) {
	return c.Do(Request{Cmd: "down", DurationMS: ms})
}
func (c *Client) Stop() (*Response, error)   { return c.Do(Request{Cmd: "stop"}) }
func (c *Client) Height() (*Response, error) { return c.Do(Request{Cmd: "height"}) }
func (c *Client) Stats() (*Response, error)  { return c.Do(Request{Cmd: "stats"}) }
func (c *Client) Info() (*Response, error)   { return c.Do(Request{Cmd: "info"}) }
func (c *Client) Status() (*Response, error) { return c.Do(Request{Cmd: "status"}) }
func (c *Client) Quit() (*Response, error)   { return c.Do(Request{Cmd: "quit"}) }

func (c *Client) Preset(n int, save bool, timeoutMS int64) (*Response, error) {
	return c.Do(Request{Cmd: "preset", Preset: n, Save: save, TimeoutMS: timeoutMS})
}
func (c *Client) Move(heightMM, toleranceMM float64, timeoutMS int64) (*Response, error) {
	return c.Do(Request{Cmd: "move", HeightMM: heightMM, ToleranceMM: toleranceMM, TimeoutMS: timeoutMS})
}
