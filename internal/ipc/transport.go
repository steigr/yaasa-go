package ipc

import "net"

// Transport abstracts the network layer used for IPC between the yaasa daemon
// and its CLI clients.
//
// The bundled [UnixTransport] is the reference implementation.  Pass a custom
// implementation to [ListenWith] / [DialWith] to use a different underlying
// channel — for example TCP, VSOCK, or an in-process [net.Pipe] for tests.
//
// # Implementing a custom transport
//
// Satisfy the three-method interface and pass an instance to [ListenWith] /
// [DialWith]:
//
//	type TCPTransport struct{ Addr string }
//
//	func (t TCPTransport) Listen() (net.Listener, error) {
//	    return net.Listen("tcp", t.Addr)
//	}
//	func (t TCPTransport) Dial() (net.Conn, error) {
//	    return net.DialTimeout("tcp", t.Addr, 2*time.Second)
//	}
//	func (t TCPTransport) String() string { return t.Addr }
//
//	// Start a server:
//	srv, err := ipc.ListenWith(TCPTransport{"127.0.0.1:9876"}, desk)
//
//	// Connect a client:
//	c, err := ipc.DialWith(TCPTransport{"127.0.0.1:9876"})
//
// The JSON request/response framing ([Request] / [Response]) is identical
// regardless of transport; only the bytes-on-wire carrier changes.
//
// # Error contract
//
//   - [Transport.Listen] must return a non-nil error if the address is already
//     in use so callers receive an actionable message.
//   - [Transport.Dial] must return a non-nil error immediately if no server is
//     listening — no silent retry loops.
type Transport interface {
	// Listen starts a server-side listener.  It must return an error if the
	// address is already in use (e.g. another daemon is already running).
	Listen() (net.Listener, error)

	// Dial opens a single client connection to the server.  It must return
	// an error immediately if no server is listening.
	Dial() (net.Conn, error)

	// String returns a human-readable description of the endpoint, used in
	// log and error messages.
	String() string
}
