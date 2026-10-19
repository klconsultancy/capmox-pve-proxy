package proxy

import (
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klconsultancy/capmox-pve-proxy/internal/config"
)

// clientAuth is the Authorization header CAPMOX sends in the tests.
const clientAuth = "PVEAPIToken=capmox@pve!capmox=client-secret"

// fakeNode is a stand-in for one Proxmox VE node: it answers from a table of
// canned responses and records every request it gets.
type fakeNode struct {
	name string
	srv  *httptest.Server

	mu        sync.Mutex
	responses map[string]canned // "METHOD /api2/json/path?query" or "METHOD /api2/json/path"
	requests  []recorded
	drop      bool                     // close the connection instead of answering
	hang      bool                     // never answer
	gates     map[string]chan struct{} // answers held back until the gate closes
	stop      chan struct{}            // closed at the end of the test
}

type canned struct {
	status int
	body   string
}

type recorded struct {
	method, path, query, auth, contentType, body string
}

func newFakeNode(t *testing.T, name string) *fakeNode {
	t.Helper()
	f := &fakeNode{name: name, responses: map[string]canned{}, gates: map[string]chan struct{}{}, stop: make(chan struct{})}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.stop) }) // runs first, so Close need not wait for held answers
	return f
}

// on sets the response to a request; key is "METHOD /path" or "METHOD /path?query",
// with the path below /api2/json.
func (f *fakeNode) on(key string, status int, body string) {
	method, path, _ := strings.Cut(key, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[method+" "+apiPrefix+path] = canned{status, body}
}

func (f *fakeNode) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: string(body),
	})
	c, ok := f.responses[r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery]
	if !ok {
		c, ok = f.responses[r.Method+" "+r.URL.Path]
	}
	drop, hang, gate := f.drop, f.hang, f.gates[r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery]
	f.mu.Unlock()
	if drop {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	if hang {
		select {
		case <-r.Context().Done():
		case <-f.stop:
		}
		return
	}
	if gate != nil {
		select {
		case <-gate:
		case <-f.stop:
			return
		}
	}
	if !ok {
		c = canned{http.StatusNotFound, `{"data":null}`}
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(c.status)
	_, _ = io.WriteString(w, c.body)
}

// dropRequests makes the node close every connection without answering, as a node
// going down does; unlike closing the server, the requests are still recorded.
func (f *fakeNode) dropRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drop = true
}

// hangRequests makes the node accept every request and never answer, as a node whose
// API hangs does.
func (f *fakeNode) hangRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = true
}

// holdAnswer holds back the answer to a request (key as in on, with the query) until
// the returned function is called.
func (f *fakeNode) holdAnswer(key string) (release func()) {
	method, path, _ := strings.Cut(key, " ")
	if !strings.Contains(path, "?") {
		path += "?"
	}
	gate := make(chan struct{})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gates[method+" "+apiPrefix+path] = gate
	return sync.OnceFunc(func() { close(gate) })
}

// waitFor waits until the node has received n requests in all.
func (f *fakeNode) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.got()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("node %s got %d requests, want %d", f.name, len(f.got()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeNode) auth() string { return "PVEAPIToken=proxy@pve!" + f.name + "=secret-" + f.name }

func (f *fakeNode) got() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

// newProxy starts the proxy in front of the fake nodes, in their order, and returns
// its URL.
func newProxy(t *testing.T, nodes ...*fakeNode) string {
	t.Helper()
	_, base := newProxyServer(t, nodes...)
	return base
}

// newProxyServer is newProxy that also returns the proxy itself.
func newProxyServer(t *testing.T, nodes ...*fakeNode) (*Server, string) {
	t.Helper()
	return newProxyServerWith(t, nil, nodes...)
}

// newProxyServerWith is newProxyServer with tune applied to the proxy before it
// serves, such as to shorten its timeouts.
func newProxyServerWith(t *testing.T, tune func(*Server), nodes ...*fakeNode) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := &config.Config{
		Listen:       ":0",
		ClusterName:  "test",
		InsecureHTTP: true,
		Client:       config.Token{ID: "capmox@pve!capmox", SecretFile: write("client", "client-secret\n")},
	}
	for _, n := range nodes {
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.srv.Certificate().Raw})
		cfg.Nodes = append(cfg.Nodes, config.Node{
			Name:   n.name,
			URL:    n.srv.URL,
			Token:  config.Token{ID: "proxy@pve!" + n.name, SecretFile: write(n.name, "secret-"+n.name)},
			CAFile: write(n.name+".pem", string(ca)),
		})
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if tune != nil {
		tune(s)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

// call sends a request to the proxy as CAPMOX would and returns the status and body.
func call(t *testing.T, base, method, path, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+apiPrefix+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", clientAuth)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data)
}
