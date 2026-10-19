package proxy

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// inventory remembers which VMIDs each node held when it last answered. It is what
// lets the proxy hand out VMIDs while a node is down: a VMID that node last held stays
// taken, any other is free on it. Without it, every VMID would have to count as taken
// and no machine could be created anywhere until the node is back; with "free"
// instead, CAPMOX would take a machine on the down node for deleted (DeleteVM treats a
// free VMID as gone) and hand its VMID out again.
//
// It lives in memory only. A proxy started while a node is down knows nothing about
// that node, and every VMID counts as taken on it until it answers again.
type inventory struct {
	mu      sync.Mutex
	vmids   map[string]map[int]bool      // node -> VMIDs it held
	fetched map[string]time.Time         // node -> when the request for its list went out
	added   map[string]map[int]time.Time // node -> VMIDs noted by add, and when
}

func newInventory() *inventory {
	return &inventory{
		vmids:   map[string]map[int]bool{},
		fetched: map[string]time.Time{},
		added:   map[string]map[int]time.Time{},
	}
}

// addedTTL is how long a VMID noted by add stays taken when the node never lists it,
// as after a clone that failed.
const addedTTL = 10 * time.Minute

// record replaces a node's list with the guests in a /cluster/resources answer to a
// request that went out at fetched. A VMID noted by add is kept until the node lists
// it itself: an answer can predate the clone, or come before Proxmox has written the
// clone's configuration. One the node has not listed within addedTTL is dropped. An
// answer older than the list it would replace is ignored.
func (inv *inventory) record(node string, resources json.RawMessage, fetched time.Time) {
	var items []struct {
		VMID *int `json:"vmid"`
	}
	if err := json.Unmarshal(resources, &items); err != nil {
		return
	}
	set := map[int]bool{}
	for _, it := range items {
		if it.VMID != nil {
			set[*it.VMID] = true
		}
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if fetched.Before(inv.fetched[node]) {
		return
	}
	for vmid, at := range inv.added[node] {
		switch {
		case set[vmid], fetched.Sub(at) > addedTTL:
			delete(inv.added[node], vmid)
		default:
			set[vmid] = true
		}
	}
	inv.vmids[node] = set
	inv.fetched[node] = fetched
}

// add notes one more VMID on a node, such as a clone the proxy just started there, so
// it counts as taken even if the node goes down before the next refresh.
func (inv *inventory) add(node string, vmid int) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if inv.added[node] == nil {
		inv.added[node] = map[int]time.Time{}
	}
	inv.added[node][vmid] = time.Now()
	if inv.vmids[node] != nil {
		inv.vmids[node][vmid] = true
	}
}

// held reports whether a node held vmid when it last answered, whether the proxy has
// a list for that node at all, and how old that list is.
func (inv *inventory) held(node string, vmid int) (held, known bool, age time.Duration) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	set, known := inv.vmids[node]
	if known {
		age = time.Since(inv.fetched[node]).Round(time.Second)
	}
	return set[vmid], known, age
}

// Refresh reads every node's guests into the inventory, every interval until ctx ends.
func (s *Server) Refresh(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()
	fetched := time.Now()
	for _, a := range s.fanOut(ctx, s.nodes, "/cluster/resources", "type=vm") {
		if a.failure() == nil {
			s.inventory.record(a.node.name, a.res.data(), fetched)
		}
	}
}
