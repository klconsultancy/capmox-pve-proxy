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
	f := &fakeNode{name: name, responses: map[string]canned{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
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
	f.mu.Unlock()
	if !ok {
		c = canned{http.StatusNotFound, `{"data":null}`}
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(c.status)
	_, _ = io.WriteString(w, c.body)
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
