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

const (
	// requestTimeout bounds a request that asks every node, however many rounds it
	// takes.
	requestTimeout = 20 * time.Second
	// answerTimeout is how long a node gets to answer a request that asks every node,
	// or one of the scheduler's reads. A node that takes longer, such as one whose
	// API hangs, counts as down: otherwise it would hold up creating machines on
	// every node.
	answerTimeout = 5 * time.Second
)

// answer is one node's answer to a request sent to several nodes.
type answer struct {
	node *node
	res  response
	err  error // the node could not be reached or did not answer
}

func (a answer) unreachable() bool { return a.err != nil }

// failure explains why the answer is not usable, or returns nil when it is.
func (a answer) failure() error {
	if a.err != nil {
		return a.err
	}
	if a.res.ok() {
		return nil
	}
	return fmt.Errorf("node '%s': HTTP %d: %s", a.node.name, a.res.status, strings.TrimSpace(string(a.res.body)))
}

// fanOut sends the same GET to the given nodes in parallel and returns their answers
// in the same order. A node that does not answer within the answer timeout is
// unreachable.
func (s *Server) fanOut(ctx context.Context, nodes []*node, path, rawQuery string) []answer {
	answers := make([]answer, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, s.answerTimeout)
			defer cancel()
			res, err := n.get(ctx, path, rawQuery)
			answers[i] = answer{node: n, res: res, err: err}
		})
	}
	wg.Wait()
	return answers
}

// version answers with the first node that responds: CAPMOX only logs the release.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	var errs []error
	for _, n := range s.nodes {
		res, err := n.get(r.Context(), "/version", "")
		a := answer{node: n, res: res, err: err}
		if a.failure() == nil {
			writeResponse(w, res)
			return
		}
		errs = append(errs, a.failure())
	}
	s.log.Error("no node answered /version", "error", errors.Join(errs...))
	writeError(w, http.StatusInternalServerError, "no node reachable")
}

// clusterStatus describes the nodes as one quorate cluster, each node online when it
// answers.
func (s *Server) clusterStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entries := []map[string]any{{
		"type": "cluster", "id": "cluster", "name": s.clusterName,
		"nodes": len(s.nodes), "quorate": 1, "version": 1,
	}}
	for i, a := range s.fanOut(ctx, s.nodes, "/version", "") {
		online := 0
		if a.failure() == nil {
			online = 1
		}
		entries = append(entries, map[string]any{
			"type": "node", "id": "node/" + a.node.name, "name": a.node.name,
			"nodeid": i + 1, "online": online, "local": 0, "ip": a.node.base.Hostname(),
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
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	fetched := time.Now()
	answers := s.fanOut(ctx, s.nodes, "/cluster/resources", r.URL.RawQuery)

	merged := []map[string]any{}
	templates := map[float64]string{} // template VMID -> node it is reported on
	owners := map[float64]string{}    // non-template VMID -> first node holding it
	answered := 0
	for _, a := range answers {
		if a.unreachable() {
			s.log.Warn("node unreachable: left out of cluster resources", "node", a.node.name, "error", a.err)
			continue
		}
		if err := a.failure(); err != nil {
			s.log.Error("cluster resources incomplete", "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		answered++
		if r.URL.Query().Get("type") == "vm" {
			s.inventory.record(a.node.name, a.res.data(), fetched)
		}
		var items []map[string]any
		if err := json.Unmarshal(a.res.data(), &items); err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Sprintf("node '%s': unreadable /cluster/resources: %v", a.node.name, err))
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
				templates[vmid] = a.node.name
			} else if first, seen := owners[vmid]; seen {
				s.log.Warn("VMID exists on several nodes", "vmid", vmid, "nodes", []string{first, a.node.name})
			} else {
				owners[vmid] = a.node.name
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
// suggestions upward. A node found unreachable is not asked again in the same request,
// so a node that is down costs one connection timeout, not one per VMID tried.
func (s *Server) nextID(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	unreachable := map[*node]bool{}

	if raw := r.URL.Query().Get("vmid"); raw != "" {
		vmid, err := strconv.Atoi(raw)
		if err != nil || vmid < 100 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid vmid '%s'", raw))
			return
		}
		free, reason, err := s.vmidFree(ctx, vmid, unreachable)
		switch {
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
		case !free:
			s.log.Info("VMID taken", "vmid", vmid, "reason", reason)
			writeVMIDTaken(w, vmid)
		default:
			writeData(w, strconv.Itoa(vmid))
		}
		return
	}

	start := 0
	for _, a := range s.fanOut(ctx, s.nodes, "/cluster/nextid", "") {
		if a.unreachable() {
			s.log.Warn("node unreachable: its VMIDs judged by its last list", "node", a.node.name, "error", a.err)
			unreachable[a.node] = true
			continue
		}
		if err := a.failure(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Proxmox sends the VMID as a string; accept a number too.
		suggested := strings.Trim(string(a.res.data()), `"`)
		id, err := strconv.Atoi(suggested)
		if err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Sprintf("node '%s': unreadable /cluster/nextid %q", a.node.name, suggested))
			return
		}
		start = max(start, id)
	}
	if start == 0 {
		writeError(w, http.StatusInternalServerError, "no node reachable")
		return
	}
	for vmid := start; vmid < start+100; vmid++ {
		free, _, err := s.vmidFree(ctx, vmid, unreachable)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if free {
			writeData(w, strconv.Itoa(vmid))
			return
		}
	}
	writeError(w, http.StatusInternalServerError, fmt.Sprintf("no VMID free on every node from %d", start))
}

// vmidFree asks every node whether vmid is free; otherwise reason says why not. A node
// that cannot be reached is judged by what it held when it last answered (see
// inventory): the VMID is taken on it if it held it then, or if the proxy has no list
// of that node at all. Nodes in unreachable are judged that way without being asked,
// and a node found unreachable is added to it. An error means no judgement is
// possible, such as when the request ran out of time.
func (s *Server) vmidFree(ctx context.Context, vmid int, unreachable map[*node]bool) (bool, string, error) {
	var ask []*node
	for _, n := range s.nodes {
		if !unreachable[n] {
			ask = append(ask, n)
		}
	}
	answers := s.fanOut(ctx, ask, "/cluster/nextid", url.Values{"vmid": {strconv.Itoa(vmid)}}.Encode())
	if err := ctx.Err(); err != nil {
		// Out of time is not unreachable: judging the nodes by the inventory now
		// could report a VMID free that one of them has taken since.
		return false, "", fmt.Errorf("checking VMID %d: %w", vmid, err)
	}
	for _, a := range answers {
		if a.unreachable() {
			s.log.Warn("node unreachable: its VMIDs judged by its last list", "node", a.node.name, "error", a.err)
			unreachable[a.node] = true
			continue
		}
		if a.res.status == http.StatusBadRequest && strings.Contains(string(a.res.body), "already exists") {
			return false, fmt.Sprintf("exists on node '%s'", a.node.name), nil
		}
		if err := a.failure(); err != nil {
			return false, err.Error(), nil
		}
	}
	for _, n := range s.nodes {
		if !unreachable[n] {
			continue
		}
		held, known, age := s.inventory.held(n.name, vmid)
		if !known {
			return false, fmt.Sprintf("node '%s' unreachable and never listed", n.name), nil
		}
		if held {
			return false, fmt.Sprintf("held by unreachable node '%s' when it was listed %s ago", n.name, age), nil
		}
	}
	return true, "", nil
}

// writeVMIDTaken answers the way a Proxmox node does for a VMID in use, which
// go-proxmox recognises by its message.
func writeVMIDTaken(w http.ResponseWriter, vmid int) {
	writeParamError(w, http.StatusBadRequest, "vmid", fmt.Sprintf("VM %d already exists", vmid))
}
