// Package valkey is a minimal client for the few commands the gateway shares across replicas (rate-limit counters,
// session affinity). It speaks RESP2, so it works with Valkey and anything protocol-compatible. Callers treat every
// error as "not shared right now" and fall back to process-local state.
package valkey

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goodtek-xyz/openllms/internal/backoff"
)

// ErrNil is the answer to GET for a missing key.
var ErrNil = errors.New("valkey: nil")

// ErrDown: a recent call failed, so calls are skipped until the back-off ends (no dial per request while the server
// is unreachable).
var ErrDown = errors.New("valkey: unavailable (backing off)")

// Defaults of the official Valkey client, GLIDE: request timeout 250 ms, connection timeout 2000 ms
// (https://glide.valkey.io/how-to/connections/timeouts-and-reconnect-strategy/; glide-core/src/client/mod.rs
// DEFAULT_RESPONSE_TIMEOUT).
const (
	DefaultRequestTimeout = 250 * time.Millisecond
	DefaultConnectTimeout = 2000 * time.Millisecond
)

// After a failed call the client waits before trying again, like GLIDE's default reconnect strategy: delays grow as
// factor·2^(k+1) ms with factor 100 over 5 retries — 200 ms up to 3200 ms — then stay at the last one
// (valkey-io/valkey-glide glide-core/redis-rs/redis/src/retry_strategies.rs @ 59aabaf34f: EXPONENT_BASE 2, FACTOR 100,
// NUMBER_OF_RETRIES 5). The jitter is full jitter (internal/backoff), like every retry loop here.
const (
	reconnectBase = 200 * time.Millisecond
	reconnectCap  = 3200 * time.Millisecond
)

const maxIdle = 8

// Client is safe for concurrent use. Connections are pooled (up to maxIdle idle) and dialed on demand.
type Client struct {
	addr           string
	user           string
	password       string
	db             int
	tls            *tls.Config
	timeout        time.Duration // per-call I/O (DefaultRequestTimeout)
	connectTimeout time.Duration // dial, TLS handshake, AUTH / SELECT (DefaultConnectTimeout)

	idle chan *conn

	mu        sync.Mutex
	downUntil time.Time
	reconnect backoff.Backoff // grows while calls keep failing; reset by any answer
	now       func() time.Time
}

type conn struct {
	nc net.Conn
	r  *bufio.Reader
}

// New parses valkey://, valkeys://, redis:// or rediss:// URLs: [user[:password]@]host[:port][/db]. Calls use
// DefaultRequestTimeout, connections DefaultConnectTimeout.
func New(rawURL string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("valkey url: %w", err)
	}
	c := &Client{
		timeout:        DefaultRequestTimeout,
		connectTimeout: DefaultConnectTimeout,
		idle:           make(chan *conn, maxIdle),
		reconnect:      backoff.Backoff{Base: reconnectBase, Cap: reconnectCap},
		now:            time.Now,
	}
	switch u.Scheme {
	case "valkey", "redis":
	case "valkeys", "rediss":
		c.tls = &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	default:
		return nil, fmt.Errorf("valkey url: scheme %q (want valkey, valkeys, redis or rediss)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("valkey url: missing host")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	c.addr = net.JoinHostPort(u.Hostname(), port)
	if u.User != nil {
		c.user = u.User.Username()
		c.password, _ = u.User.Password()
		if _, set := u.User.Password(); !set {
			// "redis://secret@host" is the common password-only form.
			c.user, c.password = "", u.User.Username()
		}
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		if c.db, err = strconv.Atoi(p); err != nil || c.db < 0 {
			return nil, fmt.Errorf("valkey url: database %q", p)
		}
	}
	return c, nil
}

// Close drops idle connections.
func (c *Client) Close() {
	for {
		select {
		case cn := <-c.idle:
			_ = cn.nc.Close()
		default:
			return
		}
	}
}

// Incr increments key and returns the new value.
func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	v, err := c.do(ctx, "INCR", key)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("valkey: INCR answered %T", v)
	}
	return n, nil
}

// PExpire sets a key's time to live.
func (c *Client) PExpire(ctx context.Context, key string, ttl time.Duration) error {
	_, err := c.do(ctx, "PEXPIRE", key, strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

// Get returns ErrNil for a missing key.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	v, err := c.do(ctx, "GET", key)
	if err != nil {
		return "", err
	}
	switch s := v.(type) {
	case nil:
		return "", ErrNil
	case string:
		return s, nil
	}
	return "", fmt.Errorf("valkey: GET answered %T", v)
}

// SetPX stores value with a time to live.
func (c *Client) SetPX(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := c.do(ctx, "SET", key, value, "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

func (c *Client) do(ctx context.Context, args ...string) (any, error) {
	c.mu.Lock()
	down := c.now().Before(c.downUntil)
	c.mu.Unlock()
	if down {
		return nil, ErrDown
	}
	cn, err := c.get(ctx)
	if err != nil {
		c.markDown()
		return nil, err
	}
	v, err := cn.call(ctx, c.timeout, args...)
	var se serverError
	switch {
	case err == nil:
		c.put(cn)
		c.markUp()
		return v, nil
	case errors.As(err, &se):
		c.put(cn) // the server answered; the connection is fine
		c.markUp()
		return nil, err
	default:
		_ = cn.nc.Close()
		c.markDown()
		return nil, err
	}
}

func (c *Client) markDown() {
	c.mu.Lock()
	c.downUntil = c.now().Add(c.reconnect.Next())
	c.mu.Unlock()
}

func (c *Client) markUp() {
	c.mu.Lock()
	c.reconnect = backoff.Backoff{Base: reconnectBase, Cap: reconnectCap}
	c.mu.Unlock()
}

func (c *Client) get(ctx context.Context) (*conn, error) {
	select {
	case cn := <-c.idle:
		return cn, nil
	default:
	}
	dctx, cancel := context.WithTimeout(ctx, c.connectTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("valkey dial: %w", err)
	}
	if c.tls != nil {
		tc := tls.Client(nc, c.tls)
		if err := tc.HandshakeContext(dctx); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("valkey tls: %w", err)
		}
		nc = tc
	}
	cn := &conn{nc: nc, r: bufio.NewReader(nc)}
	if c.password != "" {
		args := []string{"AUTH", c.password}
		if c.user != "" {
			args = []string{"AUTH", c.user, c.password}
		}
		if _, err := cn.call(ctx, c.connectTimeout, args...); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("valkey auth: %w", err)
		}
	}
	if c.db != 0 {
		if _, err := cn.call(ctx, c.connectTimeout, "SELECT", strconv.Itoa(c.db)); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("valkey select: %w", err)
		}
	}
	return cn, nil
}

func (c *Client) put(cn *conn) {
	select {
	case c.idle <- cn:
	default:
		_ = cn.nc.Close()
	}
}

type serverError string

func (e serverError) Error() string { return "valkey: " + string(e) }

func (cn *conn) call(ctx context.Context, timeout time.Duration, args ...string) (any, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := cn.nc.SetDeadline(deadline); err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(cn.nc, b.String()); err != nil {
		return nil, err
	}
	return readReply(cn.r)
}

// readReply reads one RESP2 reply: simple string, error, integer, bulk string (nil for -1). Arrays are not needed.
func readReply(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("valkey: bad reply %q", line)
	}
	body := line[1 : len(line)-2]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, serverError(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("valkey: bad integer %q", body)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 512<<20 {
			return nil, fmt.Errorf("valkey: bad bulk length %q", body)
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	}
	return nil, fmt.Errorf("valkey: unsupported reply type %q", line[0])
}
