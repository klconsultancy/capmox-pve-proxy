package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
)

// cloneParams are a clone request's parameters, in the encoding they arrived in so
// they are forwarded the same way: JSON from go-proxmox, a form from other clients.
type cloneParams struct {
	json map[string]any
	form url.Values
}

func parseCloneParams(r *http.Request, body []byte) (cloneParams, error) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "application/json" {
		p := cloneParams{json: map[string]any{}}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &p.json); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	form, err := url.ParseQuery(string(body))
	return cloneParams{form: form}, err
}

// newID returns the clone's VMID, or 0 when it has none.
func (p cloneParams) newID() int {
	var raw string
	if p.json != nil {
		switch v := p.json["newid"].(type) {
		case float64:
			return int(v)
		case string:
			raw = v
		}
	} else {
		raw = p.form.Get("newid")
	}
	id, _ := strconv.Atoi(raw)
	return id
}

func (p cloneParams) target() string {
	if p.json != nil {
		t, _ := p.json["target"].(string)
		return t
	}
	return p.form.Get("target")
}

// withoutTarget returns the parameters for a local clone, and their content type.
func (p cloneParams) withoutTarget() ([]byte, string, error) {
	if p.json != nil {
		local := make(map[string]any, len(p.json))
		for k, v := range p.json {
			if k != "target" {
				local[k] = v
			}
		}
		body, err := json.Marshal(local)
		return body, "application/json", err
	}
	local := url.Values{}
	for k, v := range p.form {
		if k != "target" {
			local[k] = v
		}
	}
	return []byte(local.Encode()), "application/x-www-form-urlencoded", nil
}

// clone serves POST /nodes/{node}/qemu/{vmid}/clone. A clone onto another node becomes
// a local clone on that node of its own copy of the template, which has the same
// VMID: standalone nodes cannot clone to each other, and even in a Proxmox cluster a
// clone to another node's local storage is not possible. The target node must
// therefore carry the template under the same VMID.
func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	src, ok := s.pathNode(w, r)
	if !ok {
		return
	}
	vmid := r.PathValue("vmid")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	params, err := parseCloneParams(r, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unreadable clone parameters: %v", err))
		return
	}

	target := params.target()
	if target == "" || target == src.name {
		s.startClone(w, r, src, vmid, params, body, r.Header.Get("Content-Type"))
		return
	}

	dst, known := s.namedNode(w, target)
	if !known {
		return
	}
	if err := s.requireTemplate(r.Context(), dst, vmid); err != nil {
		s.log.Error("clone refused", "template", vmid, "source", src.name, "target", dst.name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	local, contentType, err := params.withoutTarget()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("clone rewritten to a local clone", "template", vmid, "source", src.name, "target", dst.name)
	s.startClone(w, r, dst, vmid, params, local, contentType)
}

// startClone sends a local clone of template vmid to node n and answers with the
// node's response. A clone that starts is added at once to the inventory of the node
// it runs on, so its VMID counts as taken there even if the node goes down before the
// next refresh.
func (s *Server) startClone(w http.ResponseWriter, r *http.Request, n *node, vmid string, params cloneParams, body []byte, contentType string) {
	res, err := n.do(r.Context(), http.MethodPost, "/nodes/"+n.name+"/qemu/"+vmid+"/clone", "", body, contentType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id := params.newID(); id > 0 && res.ok() {
		s.inventory.add(n.name, id)
	}
	writeResponse(w, res)
}

// requireTemplate checks that node n carries VMID vmid as a template.
func (s *Server) requireTemplate(ctx context.Context, n *node, vmid string) error {
	res, err := n.get(ctx, "/nodes/"+n.name+"/qemu/"+vmid+"/config", "")
	if err != nil {
		return err
	}
	if !res.ok() {
		return fmt.Errorf("template %s is not on node '%s' (HTTP %d)", vmid, n.name, res.status)
	}
	var cfg map[string]any
	if err := json.Unmarshal(res.data(), &cfg); err != nil || !isTemplate(cfg) {
		return fmt.Errorf("VMID %s on node '%s' is not a template", vmid, n.name)
	}
	return nil
}
