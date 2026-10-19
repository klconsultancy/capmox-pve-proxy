package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/klconsultancy/capmox-pve-proxy/internal/config"
)

// apiPrefix is where the Proxmox VE API lives, on the nodes and on the proxy.
const apiPrefix = "/api2/json"

// node is one standalone Proxmox VE node and the client the proxy reaches it with.
type node struct {
	name   string
	base   *url.URL // https://host:8006, without the API prefix
	auth   string   // the Authorization header value for this node's token
	client *http.Client
}

func newNode(n config.Node) (*node, error) {
	base, err := url.Parse(n.URL)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimSuffix(strings.TrimSuffix(base.Path, "/"), apiPrefix)
	auth, err := n.Token.Authorization()
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", n.Name, err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A node that is down must not hold up the requests that ask every node: a node
	// switched off on the LAN otherwise takes the full dial timeout to fail.
	transport.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 5 * time.Second
	if n.CAFile != "" {
		pem, err := os.ReadFile(n.CAFile)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", n.Name, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("node %s: no certificate in %s", n.Name, n.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &node{
		name:   n.Name,
		base:   base,
		auth:   auth,
		client: &http.Client{Transport: transport, Timeout: 60 * time.Second},
	}, nil
}

// url returns the node's URL for an API path (without the prefix) and raw query.
func (n *node) url(path, rawQuery string) *url.URL {
	u := *n.base
	u.Path = u.Path + apiPrefix + path
	u.RawPath = ""
	u.RawQuery = rawQuery
	return &u
}

// response is a Proxmox API response, read whole: the cluster-wide endpoints
// combine several of them.
type response struct {
	status int
	body   []byte
}

// data returns the response's `data` member.
func (r response) data() json.RawMessage {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(r.body, &env)
	return env.Data
}

func (r response) ok() bool { return r.status >= 200 && r.status < 300 }

// do sends one request to the node and reads the whole response. An error means the
// node could not be reached or did not answer.
func (n *node) do(ctx context.Context, method, path, rawQuery string, body []byte, contentType string) (response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.url(path, rawQuery).String(), reader)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Authorization", n.auth)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := n.client.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("node %s: %w", n.name, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return response{}, fmt.Errorf("node %s: %w", n.name, err)
	}
	return response{status: res.StatusCode, body: data}, nil
}

// get is do for a GET without a body.
func (n *node) get(ctx context.Context, path, rawQuery string) (response, error) {
	return n.do(ctx, http.MethodGet, path, rawQuery, nil, "")
}
