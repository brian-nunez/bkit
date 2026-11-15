package valkey

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is an in-process server that speaks enough RESP3 for the
// valkey-go client and the commands the driver sends. The tests run the real
// client against it, so no Valkey server is needed.
type fakeServer struct {
	ln net.Listener

	mu       sync.Mutex
	data     map[string]entry
	fail     map[string]string // command to the error reply it gets
	pageSize int
	cursors  map[uint64]string // SCAN cursor to the last key it returned
	next     uint64
	seen     [][]string // every command received, for assertions
}

type entry struct {
	value   string
	expires time.Time
}

// startServer starts a fakeServer on a free local port.
func startServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{
		ln:       ln,
		data:     map[string]entry{},
		fail:     map[string]string{},
		pageSize: 3,
		cursors:  map[uint64]string{},
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *fakeServer) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	for {
		cmd, err := readCommand(r)
		if err != nil {
			return
		}
		s.reply(w, cmd)
		if r.Buffered() == 0 {
			if err := w.Flush(); err != nil {
				return
			}
		}
	}
}

// readCommand reads one array of bulk strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || line[0] != '*' {
		return nil, fmt.Errorf("bad request %q", line)
	}
	out := make([]string, n)
	for i := range n {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, _ := strconv.Atoi(strings.TrimSpace(hdr[1:]))
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out[i] = string(buf[:size])
	}
	return out, nil
}

func bulk(s string) string     { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }
func integer(n int64) string   { return ":" + strconv.FormatInt(n, 10) + "\r\n" }
func boolInt(b bool) string    { return map[bool]string{true: ":1\r\n", false: ":0\r\n"}[b] }
func errReply(m string) string { return "-" + m + "\r\n" }

const null = "_\r\n"

// live returns the unexpired entry for key. The caller holds s.mu.
func (s *fakeServer) live(key string) (entry, bool) {
	e, ok := s.data[key]
	if ok && !e.expires.IsZero() && !time.Now().Before(e.expires) {
		delete(s.data, key)
		return entry{}, false
	}
	return e, ok
}

func (s *fakeServer) reply(w *bufio.Writer, cmd []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, cmd)
	name := strings.ToUpper(cmd[0])
	if msg, ok := s.fail[name]; ok {
		_, _ = w.WriteString(errReply(msg))
		return
	}
	_, _ = w.WriteString(s.run(name, cmd[1:]))
}

func (s *fakeServer) run(name string, args []string) string {
	switch name {
	case "HELLO":
		return "%3\r\n" + bulk("server") + bulk("valkey") + bulk("version") + bulk("9.0.0") + bulk("proto") + integer(3)
	case "PING":
		return "+PONG\r\n"
	case "CLIENT", "SELECT", "AUTH":
		return "+OK\r\n"
	case "GET":
		if e, ok := s.live(args[0]); ok {
			return bulk(e.value)
		}
		return null
	case "SET":
		key, value, nx, ttl := args[0], args[1], false, time.Duration(0)
		for i := 2; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "NX":
				nx = true
			case "PX":
				ms, _ := strconv.ParseInt(args[i+1], 10, 64)
				ttl = time.Duration(ms) * time.Millisecond
				i++
			}
		}
		if _, ok := s.live(key); ok && nx {
			return null
		}
		e := entry{value: value}
		if ttl > 0 {
			e.expires = time.Now().Add(ttl)
		}
		s.data[key] = e
		return "+OK\r\n"
	case "DEL", "EXISTS":
		var n int64
		for _, k := range args {
			if _, ok := s.live(k); ok {
				n++
			}
			if name == "DEL" {
				delete(s.data, k)
			}
		}
		return integer(n)
	case "INCRBY":
		e, ok := s.live(args[0])
		var n int64
		if ok {
			var err error
			if n, err = strconv.ParseInt(e.value, 10, 64); err != nil {
				return errReply("ERR value is not an integer or out of range")
			}
		}
		delta, _ := strconv.ParseInt(args[1], 10, 64)
		sum := n + delta
		if (delta > 0 && sum < n) || (delta < 0 && sum > n) {
			return errReply("ERR increment or decrement would overflow")
		}
		e.value = strconv.FormatInt(sum, 10)
		s.data[args[0]] = e
		return integer(sum)
	case "PTTL":
		e, ok := s.live(args[0])
		switch {
		case !ok:
			return integer(-2)
		case e.expires.IsZero():
			return integer(-1)
		}
		return integer(time.Until(e.expires).Milliseconds())
	case "PEXPIRE":
		e, ok := s.live(args[0])
		if !ok {
			return boolInt(false)
		}
		ms, _ := strconv.ParseInt(args[1], 10, 64)
		e.expires = time.Now().Add(time.Duration(ms) * time.Millisecond)
		s.data[args[0]] = e
		return boolInt(true)
	case "PERSIST":
		e, ok := s.live(args[0])
		if !ok || e.expires.IsZero() {
			return boolInt(false)
		}
		e.expires = time.Time{}
		s.data[args[0]] = e
		return boolInt(true)
	case "SCAN":
		return s.scan(args)
	}
	return errReply("ERR unknown command '" + name + "'")
}

// scan answers SCAN cursor MATCH pattern COUNT n for patterns the driver
// builds: an escaped prefix followed by "*". Like a real cursor, resuming
// after the last returned key does not skip keys deleted between calls.
func (s *fakeServer) scan(args []string) string {
	cursor, _ := strconv.ParseUint(args[0], 10, 64)
	prefix := unescape(strings.TrimSuffix(args[2], "*"))
	var keys []string
	for k := range s.data {
		if _, ok := s.live(k); ok && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if cursor != 0 {
		after := s.cursors[cursor]
		keys = slices.DeleteFunc(keys, func(k string) bool { return k <= after })
	}
	page := keys[:min(s.pageSize, len(keys))]
	next := uint64(0)
	if len(page) < len(keys) {
		s.next++
		next = s.next
		s.cursors[next] = page[len(page)-1]
	}
	var b strings.Builder
	b.WriteString("*2\r\n" + bulk(strconv.FormatUint(next, 10)))
	b.WriteString("*" + strconv.Itoa(len(page)) + "\r\n")
	for _, k := range page {
		b.WriteString(bulk(k))
	}
	return b.String()
}

func unescape(p string) string {
	var sb strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
		}
		sb.WriteByte(p[i])
	}
	return sb.String()
}

// commands returns the commands received so far with the given name.
func (s *fakeServer) commands(name string) [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]string
	for _, c := range s.seen {
		if strings.EqualFold(c[0], name) {
			out = append(out, c)
		}
	}
	return out
}
