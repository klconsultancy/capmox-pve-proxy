// Package proxy implements the part of the Proxmox VE API that CAPMOX calls, on top
// of several standalone nodes presented as one cluster.
package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/klconsultancy/capmox-pve-proxy/internal/config"
)

// Server is the proxy's HTTP handler.
type Server struct {
	clusterName string
	clientAuth  string // the Authorization header value CAPMOX must send
	nodes       []*node
	byName      map[string]*node
	log         *slog.Logger
	mux         *http.ServeMux
}

// New builds the proxy from its configuration, reading the tokens' secrets.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	secret, err := cfg.Client.ReadSecret()
	if err != nil {
		return nil, fmt.Errorf("client token: %w", err)
	}
	s := &Server{
		clusterName: cfg.ClusterName,
		clientAuth:  "PVEAPIToken=" + cfg.Client.ID + "=" + secret,
		byName:      map[string]*node{},
		log:         log,
		mux:         http.NewServeMux(),
	}
	for _, nc := range cfg.Nodes {
		n, err := newNode(nc)
		if err != nil {
			return nil, err
		}
		s.nodes = append(s.nodes, n)
		s.byName[n.name] = n
	}
	s.routes()
	return s, nil
}

// routes registers every endpoint CAPMOX calls (CAPMOX v0.9, go-proxmox v0.8). Anything
// else is refused: see ServeHTTP.
func (s *Server) routes() {
	// Answered by the proxy for all nodes together.
	s.handle("GET /version", s.version)
	s.handle("GET /cluster/status", s.clusterStatus)
	s.handle("GET /cluster/resources", s.clusterResources)
	s.handle("GET /cluster/nextid", s.nextID)
	s.handle("POST /nodes/{node}/qemu/{vmid}/clone", s.clone)

	// Sent to the node in the path, unchanged.
	for _, route := range []string{
		"GET /nodes/{node}/status",
		"GET /nodes/{node}/qemu",
		"GET /nodes/{node}/lxc",
		"GET /nodes/{node}/qemu/{vmid}/status/current",
		"GET /nodes/{node}/qemu/{vmid}/config",
		"POST /nodes/{node}/qemu/{vmid}/config",
		"PUT /nodes/{node}/qemu/{vmid}/resize",
		"POST /nodes/{node}/qemu/{vmid}/status/start",
		"POST /nodes/{node}/qemu/{vmid}/status/stop",
		"POST /nodes/{node}/qemu/{vmid}/status/resume",
		"DELETE /nodes/{node}/qemu/{vmid}",
		"GET /nodes/{node}/qemu/{vmid}/agent/get-osinfo",
		"POST /nodes/{node}/qemu/{vmid}/agent/exec",
		"GET /nodes/{node}/qemu/{vmid}/agent/exec-status",
		"GET /nodes/{node}/storage",
		"GET /nodes/{node}/storage/{storage}/status",
		"POST /nodes/{node}/storage/{storage}/upload",
		// A volume ID such as local:iso/user-data-101.iso spans path segments.
		"GET /nodes/{node}/storage/{storage}/content/{volume...}",
		"DELETE /nodes/{node}/storage/{storage}/content/{volume...}",
	} {
		s.handle(route, s.forwardToPathNode)
	}
	// A task lives on the node its UPID names.
	s.handle("GET /nodes/{node}/tasks/{upid}/status", s.forwardTask)
}

func (s *Server) handle(pattern string, h http.HandlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	s.mux.HandleFunc(method+" "+apiPrefix+path, h)
}

// ServeHTTP authenticates the request, then serves it if the proxy implements the
// endpoint. An endpoint it does not implement is refused and logged, so a CAPMOX
// release that needs a new call fails visibly instead of half-working.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond))
	}()

	got := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.clientAuth)) != 1 {
		writeError(rec, http.StatusUnauthorized, "authentication failure")
		return
	}
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.log.Warn("refused: endpoint not implemented", "method", r.Method, "path", r.URL.Path)
		writeError(rec, http.StatusNotImplemented,
			fmt.Sprintf("capmox-pve-proxy does not implement %s %s", r.Method, r.URL.Path))
		return
	}
	s.mux.ServeHTTP(rec, r)
}

// pathNode returns the node named in the request path, or writes an error.
func (s *Server) pathNode(w http.ResponseWriter, r *http.Request) (*node, bool) {
	name := r.PathValue("node")
	n, ok := s.byName[name]
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("no such node '%s'", name))
	}
	return n, ok
}

func (s *Server) forwardToPathNode(w http.ResponseWriter, r *http.Request) {
	if n, ok := s.pathNode(w, r); ok {
		s.forward(w, r, n, strings.TrimPrefix(r.URL.Path, apiPrefix))
	}
}

func (s *Server) forwardTask(w http.ResponseWriter, r *http.Request) {
	upid := r.PathValue("upid")
	name, ok := upidNode(upid)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unable to parse UPID '%s'", upid))
		return
	}
	n, known := s.byName[name]
	if !known {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("no such node '%s'", name))
		return
	}
	s.forward(w, r, n, "/nodes/"+name+"/tasks/"+upid+"/status")
}

// upidNode returns the node of a task ID: UPID:<node>:<pid>:<pstart>:<starttime>:<type>:<id>:<user>:
func upidNode(upid string) (string, bool) {
	parts := strings.Split(upid, ":")
	if len(parts) < 8 || parts[0] != "UPID" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// forward streams the request to a node with the node's own token, and its response
// back. Request bodies such as an ISO upload are streamed, not buffered.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, n *node, path string) {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = n.url(path, r.URL.RawQuery)
			pr.Out.Host = n.base.Host
			pr.Out.Header.Set("Authorization", n.auth)
		},
		Transport:      n.client.Transport,
		ModifyResponse: normalizeStatus,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			s.log.Error("node unreachable", "node", n.name, "error", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("node '%s' unreachable", n.name))
		},
	}
	rp.ServeHTTP(w, r)
}

// normalizeStatus turns a node's error status that go-proxmox would not recognise as
// an error into 500. go-proxmox treats only 400, 401, 403, 500 and 501 as errors: a
// 404, 502 or Proxmox's own 595 ("no route to host") would otherwise reach CAPMOX as
// success with empty data.
func normalizeStatus(res *http.Response) error {
	res.StatusCode = clientStatus(res.StatusCode)
	res.Status = fmt.Sprintf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))
	return nil
}

func clientStatus(status int) int {
	switch {
	case status < 400:
		return status
	case status == http.StatusBadRequest, status == http.StatusUnauthorized,
		status == http.StatusForbidden, status == http.StatusNotImplemented:
		return status
	default:
		return http.StatusInternalServerError
	}
}

// writeError answers in the shape Proxmox uses: no data, and the message in the status
// line and the body.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	body := map[string]any{"data": nil, "message": message}
	if status == http.StatusBadRequest {
		body["errors"] = map[string]string{"proxy": message}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// writeData answers with a successful Proxmox response around data.
func writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// writeResponse copies a node's whole response, with its status normalized.
func writeResponse(w http.ResponseWriter, res response) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(clientStatus(res.status))
	_, _ = w.Write(res.body)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	return io.Copy(r.ResponseWriter, src)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
