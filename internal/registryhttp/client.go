// Package registryhttp owns bounded network I/O for Mu-managed registries and
// downloads. Transfers may run indefinitely while making progress; stalled I/O
// is bounded without imposing a short whole-file deadline on large artifacts.
package registryhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

const LookupTimeout = 2 * time.Second

var ErrOffline = errors.New("remote access disabled by --offline")

type offlineKey struct{}

func WithOffline(ctx context.Context) context.Context {
	return context.WithValue(ctx, offlineKey{}, true)
}
func IsOffline(ctx context.Context) bool { v, _ := ctx.Value(offlineKey{}).(bool); return v }

type Options struct{ ConnectTimeout, HeaderTimeout, IdleTimeout time.Duration }

func NewClient(options Options) *http.Client {
	if options.ConnectTimeout <= 0 {
		options.ConnectTimeout = 3 * time.Second
	}
	if options.HeaderTimeout <= 0 {
		options.HeaderTimeout = 5 * time.Second
	}
	if options.IdleTimeout <= 0 {
		options.IdleTimeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Per-connection progress bounds require one response stream per connection.
	// HTTP/1.1 keeps deadlines attributable to this transfer instead of another
	// multiplexed request refreshing a silent stream's deadline.
	transport.ForceAttemptHTTP2 = false
	transport.ResponseHeaderTimeout = options.HeaderTimeout
	transport.TLSHandshakeTimeout = options.ConnectTimeout
	dialer := &net.Dialer{Timeout: options.ConnectTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &progressConn{Conn: conn, idle: options.IdleTimeout}, nil
	}
	return &http.Client{Transport: offlineTransport{transport}}
}

type offlineTransport struct{ inner http.RoundTripper }

func (t offlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if IsOffline(req.Context()) {
		return nil, ErrOffline
	}
	return t.inner.RoundTrip(req)
}

type progressConn struct {
	net.Conn
	idle time.Duration
}

func (c *progressConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
func (c *progressConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
