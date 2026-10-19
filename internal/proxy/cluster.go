package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// result is one node's answer to a request sent to every node.
type result struct {
	node *node
	res  response
	err  error
}

// all sends the same GET to every node at once and returns their answers in node order.
func (s *Server) all(ctx context.Context, path, rawQuery string) []result {
	results := make([]result, len(s.nodes))
	var wg sync.WaitGroup
	for i, n := range s.nodes {
		wg.Go(func() {
			res, err := n.get(ctx, path, rawQuery)
			results[i] = result{node: n, res: res, err: err}
		})
	}
	wg.Wait()
	return results
}

// version answers with the first node that responds: CAPMOX only logs the release.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	var errs []error
	for _, n := range s.nodes {
		res, err := n.get(r.Context(), "/version", "")
		if err == nil && res.ok() {
			writeResponse(w, res)
			return
		}
		errs = append(errs, describe(n, res, err))
	}
	s.log.Error("no node answered /version", "error", errors.Join(errs...))
	writeError(w, http.StatusInternalServerError, "no node reachable")
}

// clusterStatus describes the nodes as one quorate cluster, each node online when it
// answers.
func (s *Server) clusterStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	results := s.all(ctx, "/version", "")

	entries := []map[string]any{{
		"type": "cluster", "id": "cluster", "name": s.clusterName,
		"nodes": len(s.nodes), "quorate": 1, "version": 1,
	}}
	for i, res := range results {
		online := 0
		if res.err == nil && res.res.ok() {
			online = 1
		}
		entries = append(entries, map[string]any{
			"type": "node", "id": "node/" + res.node.name, "name": res.node.name,
			"nodeid": i + 1, "online": online, "local": 0, "ip": res.node.base.Hostname(),
		})
	}
	writeData(w, entries)
}

// clusterResources merges every node's resources. A template that exists on several
// nodes under the same VMID is reported once, on the first of them in the
// configuration that answers: CAPMOX requires exactly one template per tag set, and
// the clone rewrite finds the copy on the target node. Any other VMID on several nodes
// is reported for each and logged.
//
// A node that cannot be reached is left out, so CAPMOX still finds a template on the
// nodes that are up. CAPMOX reads this list only to find templates and to locate a VM
// it lost track of; a VM missing from it makes the latter retry, not delete anything.
// A node that answers with an error fails the request.
func (s *Server) clusterResources(w http.ResponseWriter, r *http.Request) {
	results := s.all(r.Context(), "/cluster/resources", r.URL.RawQuery)

	merged := []map[string]any{}
	templates := map[float64]string{} // template VMID -> node it is reported on
	owners := map[float64]string{}    // non-template VMID -> first node holding it
	answered := 0
	for _, res := range results {
		if res.err != nil {
			s.log.Warn("node unreachable: left out of cluster resources", "node", res.node.name, "error", res.err)
			continue
		}
		if !res.res.ok() {
			err := describe(res.node, res.res, nil)
			s.log.Error("cluster resources incomplete", "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		answered++
		if r.URL.Query().Get("type") == "vm" {
			s.inventory.record(res.node.name, res.res.data())
		}
		var items []map[string]any
		if err := json.Unmarshal(res.res.data(), &items); err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Sprintf("node '%s': unreadable /cluster/resources: %v", res.node.name, err))
			return
		}
		for _, item := range items {
			vmid, isGuest := item["vmid"].(float64)
			if !isGuest {
				merged = append(merged, item)
				continue
			}
			if isTemplate(item) {
				if _, seen := templates[vmid]; seen {
					continue
				}
				templates[vmid] = res.node.name
			} else if first, seen := owners[vmid]; seen {
				s.log.Warn("VMID exists on several nodes", "vmid", vmid, "nodes", []string{first, res.node.name})
			} else {
				owners[vmid] = res.node.name
			}
			merged = append(merged, item)
		}
	}
	if answered == 0 {
		writeError(w, http.StatusInternalServerError, "no node reachable")
		return
	}
	writeData(w, merged)
}

func isTemplate(item map[string]any) bool {
	switch t := item["template"].(type) {
	case float64:
		return t == 1
	case bool:
		return t
	}
	return false
}

// nextID reports a VMID as free only when it is free on every node; see vmidFree for
// a node that cannot be reached. With `vmid`, it checks that one. Without, it returns
// the lowest VMID free on every node, from the highest of the reachable nodes' own
// suggestions upward.
func (s *Server) nextID(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("vmid"); raw != "" {
		vmid, err := strconv.Atoi(raw)
		if err != nil || vmid < 100 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid vmid '%s'", raw))
			return
		}
		if free, reason := s.vmidFree(r.Context(), vmid); !free {
			s.log.Info("VMID taken", "vmid", vmid, "reason", reason)
			writeVMIDTaken(w, vmid)
			return
		}
		writeData(w, strconv.Itoa(vmid))
		return
	}

	start := 0
	for _, res := range s.all(r.Context(), "/cluster/nextid", "") {
		if res.err != nil {
			continue // vmidFree accounts for a node that cannot be reached
		}
		if !res.res.ok() {
			err := describe(res.node, res.res, res.err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Proxmox sends the VMID as a string; accept a number too.
		suggested := strings.Trim(string(res.res.data()), `"`)
		id, err := strconv.Atoi(suggested)
		if err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Sprintf("node '%s': unreadable /cluster/nextid %q", res.node.name, suggested))
			return
		}
		start = max(start, id)
	}
	if start == 0 {
		writeError(w, http.StatusInternalServerError, "no node reachable")
		return
	}
	for vmid := start; vmid < start+100; vmid++ {
		if free, _ := s.vmidFree(r.Context(), vmid); free {
			writeData(w, strconv.Itoa(vmid))
			return
		}
	}
	writeError(w, http.StatusInternalServerError, fmt.Sprintf("no VMID free on every node from %d", start))
}

// vmidFree asks every node whether vmid is free; otherwise reason says why not. A node
// that cannot be reached is judged by what it held when it last answered (see
// inventory): the VMID is taken on it if it held it then, or if the proxy has no list
// of that node at all.
func (s *Server) vmidFree(ctx context.Context, vmid int) (bool, string) {
	query := url.Values{"vmid": {strconv.Itoa(vmid)}}.Encode()
	for _, res := range s.all(ctx, "/cluster/nextid", query) {
		switch {
		case res.err != nil:
			held, known := s.inventory.held(res.node.name, vmid)
			if !known {
				return false, fmt.Sprintf("node '%s' unreachable and never listed", res.node.name)
			}
			if held {
				return false, fmt.Sprintf("held by unreachable node '%s' when it last answered", res.node.name)
			}
		case res.res.status == http.StatusBadRequest && strings.Contains(string(res.res.body), "already exists"):
			return false, fmt.Sprintf("exists on node '%s'", res.node.name)
		case !res.res.ok():
			return false, describe(res.node, res.res, nil).Error()
		}
	}
	return true, ""
}

// writeVMIDTaken answers the way a Proxmox node does for a VMID in use, which
// go-proxmox recognises by its message.
func writeVMIDTaken(w http.ResponseWriter, vmid int) {
	msg := fmt.Sprintf("VM %d already exists", vmid)
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": map[string]string{"vmid": msg}})
}

// describe explains why a node's answer is not usable.
func describe(n *node, res response, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("node '%s': HTTP %d: %s", n.name, res.status, strings.TrimSpace(string(res.body)))
}
