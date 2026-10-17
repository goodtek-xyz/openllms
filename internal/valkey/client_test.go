package valkey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks enough RESP2 for the client: AUTH, SELECT, INCR, PEXPIRE, GET, SET … PX. It records commands.
type fakeServer struct {
	ln       net.Listener
	password string
	mu       sync.Mutex
	kv       map[string]string
	cmds     []string
}

func startFake(t *testing.T, password string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{ln: ln, password: password, kv: map[string]string{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	authed := f.password == ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
		args := make([]string, n)
		for i := range args {
			l, _ := r.ReadString('\n')
			size, _ := strconv.Atoi(strings.TrimSpace(l[1:]))
			buf := make([]byte, size+2)
			_, _ = r.Read(buf)
			args[i] = string(buf[:size])
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, strings.ToUpper(args[0]))
		reply := "-ERR unknown\r\n"
		switch strings.ToUpper(args[0]) {
		case "AUTH":
			if args[len(args)-1] == f.password {
				authed, reply = true, "+OK\r\n"
			} else {
				reply = "-WRONGPASS invalid password\r\n"
			}
		case "SELECT":
			reply = "+OK\r\n"
		default:
			if !authed {
				reply = "-NOAUTH Authentication required.\r\n"
				break
			}
			switch strings.ToUpper(args[0]) {
			case "INCR":
				v, _ := strconv.Atoi(f.kv[args[1]])
				v++
				f.kv[args[1]] = strconv.Itoa(v)
				reply = fmt.Sprintf(":%d\r\n", v)
			case "PEXPIRE":
				reply = ":1\r\n"
			case "GET":
				if v, ok := f.kv[args[1]]; ok {
					reply = fmt.Sprintf("$%d\r\n%s\r\n", len(v), v)
				} else {
					reply = "$-1\r\n"
				}
			case "SET":
				f.kv[args[1]] = args[2]
				reply = "+OK\r\n"
			}
		}
		f.mu.Unlock()
		if _, err := c.Write([]byte(reply)); err != nil {
			return
		}
	}
}

func (f *fakeServer) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func TestClientCommands(t *testing.T) {
	f := startFake(t, "s3cret")
	c, err := New("valkey://:s3cret@" + f.ln.Addr().String() + "/2")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		if n, err := c.Incr(ctx, "k"); err != nil || n != want {
			t.Fatalf("INCR = %d, %v; want %d", n, err, want)
		}
	}
	if err := c.PExpire(ctx, "k", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "missing"); !errors.Is(err, ErrNil) {
		t.Fatalf("GET missing: %v, want ErrNil", err)
	}
	if err := c.SetPX(ctx, "a", "value with spaces\r\n", time.Hour); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, "a"); err != nil || v != "value with spaces\r\n" {
		t.Fatalf("GET a = %q, %v", v, err)
	}
	cmds := strings.Join(f.commands(), " ")
	if !strings.HasPrefix(cmds, "AUTH SELECT INCR") || strings.Count(cmds, "AUTH") != 1 {
		t.Fatalf("commands %q: want one AUTH + SELECT on a pooled connection, then the calls", cmds)
	}
}

func TestClientWrongPasswordAndServerErrors(t *testing.T) {
	f := startFake(t, "right")
	c, _ := New("redis://wrong@" + f.ln.Addr().String())
	if _, err := c.Incr(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("wrong password: %v", err)
	}
}

// After a failed call the client skips calls for a back-off that grows like GLIDE's reconnect delays (ceiling
// 200 ms, doubling, capped at 3200 ms; full jitter below the ceiling), and any answer resets it.
func TestClientBacksOffWhenDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens: dials fail
	c, _ := New("valkey://" + addr)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	ceilings := []time.Duration{200, 400, 800, 1600, 3200, 3200, 3200}
	for i, ceil := range ceilings {
		ceil *= time.Millisecond
		if _, err := c.Incr(context.Background(), "k"); err == nil || errors.Is(err, ErrDown) {
			t.Fatalf("attempt %d: %v, want a dial error", i+1, err)
		}
		wait := c.downUntil.Sub(now)
		if wait < 0 || wait > ceil {
			t.Fatalf("attempt %d: back-off %v, want within [0, %v]", i+1, wait, ceil)
		}
		if wait > 0 {
			if _, err := c.Incr(context.Background(), "k"); !errors.Is(err, ErrDown) {
				t.Fatalf("attempt %d within back-off: %v, want ErrDown (no dial)", i+1, err)
			}
		}
		now = now.Add(wait + time.Millisecond)
	}
	if c.connectTimeout != DefaultConnectTimeout || c.timeout != DefaultRequestTimeout {
		t.Fatalf("timeouts %v / %v, want GLIDE defaults", c.connectTimeout, c.timeout)
	}
}

// Any server answer resets the back-off to its first step.
func TestClientBackoffResetsOnAnswer(t *testing.T) {
	f := startFake(t, "")
	c, _ := New("valkey://" + f.ln.Addr().String())
	c.reconnect.Next()
	c.reconnect.Next() // as if two failures had happened
	if _, err := c.Incr(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if got := c.reconnect.Next(); got > reconnectBase {
		t.Fatalf("after an answer the next back-off is %v, want within the first step (%v)", got, reconnectBase)
	}
}

func TestNewRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"http://h:6379", "valkey://", "valkey://h/notadb", "::"} {
		if _, err := New(u); err == nil {
			t.Errorf("%q: want error", u)
		}
	}
	c, err := New("rediss://user:pw@cache.example:6380/1")
	if err != nil || c.addr != "cache.example:6380" || c.user != "user" || c.password != "pw" || c.db != 1 || c.tls == nil {
		t.Fatalf("rediss parse: %+v, %v", c, err)
	}
}

// TestRealValkey runs against a real server when VALKEY_TEST_URL is set (e.g. valkey://127.0.0.1:6390).
func TestRealValkey(t *testing.T) {
	url := os.Getenv("VALKEY_TEST_URL")
	if url == "" {
		t.Skip("VALKEY_TEST_URL not set")
	}
	c, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	key := fmt.Sprintf("llms:test:%d", time.Now().UnixNano())
	if n, err := c.Incr(ctx, key); err != nil || n != 1 {
		t.Fatalf("INCR = %d, %v", n, err)
	}
	if err := c.PExpire(ctx, key, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPX(ctx, key+":s", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, key+":s"); err != nil || v != "v" {
		t.Fatalf("GET = %q, %v", v, err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := c.Get(ctx, key); !errors.Is(err, ErrNil) {
		t.Fatalf("expired key: %v, want ErrNil", err)
	}
}
