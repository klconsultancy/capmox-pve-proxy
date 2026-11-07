package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer that the server's goroutines may write to while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records decodes the JSON records in out, failing on any line that is not one.
func records(t *testing.T, out string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q is not a JSON record: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestErrorLogLevels(t *testing.T) {
	for _, tc := range []struct{ line, level, remote string }{
		{"http: TLS handshake error from 10.0.0.5:51234: EOF", "DEBUG", "10.0.0.5:51234"},
		{"http: TLS handshake error from 10.0.0.5:51234: read tcp 10.0.0.9:8006->10.0.0.5:51234: read: connection reset by peer", "DEBUG", "10.0.0.5:51234"},
		{"http: TLS handshake error from [fd00::5]:51234: EOF", "DEBUG", "[fd00::5]:51234"},
		{"http: TLS handshake error from 10.0.0.5:51234: tls: first record does not look like a TLS handshake", "WARN", "10.0.0.5:51234"},
		{"http: TLS handshake error from 10.0.0.5:51234: remote error: tls: unknown certificate", "WARN", "10.0.0.5:51234"},
		{"http: panic serving 10.0.0.5:51234: boom\ngoroutine 7 [running]:\n", "ERROR", "10.0.0.5:51234"},
		{"http: Accept error: accept tcp [::]:8006: accept: too many open files; retrying in 5ms", "ERROR", ""},
	} {
		var out bytes.Buffer
		l := newErrorLog(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
		l.Print(tc.line)

		recs := records(t, out.String())
		if len(recs) != 1 {
			t.Fatalf("%q: %d records, want 1", tc.line, len(recs))
		}
		rec := recs[0]
		if rec["level"] != tc.level {
			t.Errorf("%q: level %v, want %s", tc.line, rec["level"], tc.level)
		}
		remote, ok := rec["remote"]
		if tc.remote == "" && ok || tc.remote != "" && remote != tc.remote {
			t.Errorf("%q: remote %v, want %q", tc.line, remote, tc.remote)
		}
		if rec["error"] != strings.TrimSuffix(tc.line, "\n") {
			t.Errorf("%q: error %q, want the line", tc.line, rec["error"])
		}
	}
}

// TestBareConnectIsLoggedAtDebug checks what a kubelet tcpSocket probe or a port
// scanner leaves in the log: a debug record only, and nothing through the standard log.
func TestBareConnectIsLoggedAtDebug(t *testing.T) {
	var std syncBuffer
	log.SetOutput(&std)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var out syncBuffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = newServer("", http.NotFoundHandler(), logger)
	ts.StartTLS()
	t.Cleanup(ts.Close)

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	for deadline := time.Now().Add(5 * time.Second); out.String() == ""; {
		if time.Now().After(deadline) {
			t.Fatal("no record of the connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ts.Close()

	recs := records(t, out.String())
	if len(recs) != 1 || recs[0]["level"] != "DEBUG" || recs[0]["remote"] != conn.LocalAddr().String() {
		t.Errorf("records %v, want one debug record with the client's address", recs)
	}
	if s := std.String(); s != "" {
		t.Errorf("standard log %q, want nothing", s)
	}
}
