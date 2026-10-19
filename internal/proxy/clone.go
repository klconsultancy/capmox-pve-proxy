package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
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
		res, err := src.do(r.Context(), http.MethodPost, "/nodes/"+src.name+"/qemu/"+vmid+"/clone",
			"", body, r.Header.Get("Content-Type"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeResponse(w, res)
		return
	}

	dst, known := s.byName[target]
	if !known {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("no such node '%s'", target))
		return
	}
	if err := s.requireTemplate(r, dst, vmid); err != nil {
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
	res, err := dst.do(r.Context(), http.MethodPost, "/nodes/"+dst.name+"/qemu/"+vmid+"/clone",
		"", local, contentType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeResponse(w, res)
}

// requireTemplate checks that node n carries VMID vmid as a template.
func (s *Server) requireTemplate(r *http.Request, n *node, vmid string) error {
	res, err := n.get(r.Context(), "/nodes/"+n.name+"/qemu/"+vmid+"/config", "")
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
