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
	mu    sync.Mutex
	vmids map[string]map[int]bool // node -> VMIDs it held
	seen  map[string]time.Time    // node -> when its list was taken
}

func newInventory() *inventory {
	return &inventory{vmids: map[string]map[int]bool{}, seen: map[string]time.Time{}}
}

// record replaces a node's list with the guests in a /cluster/resources answer.
func (inv *inventory) record(node string, resources json.RawMessage) {
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
	inv.vmids[node] = set
	inv.seen[node] = time.Now()
}

// add notes one more VMID on a node, such as a clone the proxy just started there, so
// it counts as taken even if the node goes down before the next refresh.
func (inv *inventory) add(node string, vmid int) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if inv.vmids[node] == nil {
		return // no list yet: the node counts as holding every VMID anyway
	}
	inv.vmids[node][vmid] = true
}

// held reports whether a node held vmid when it last answered, and whether the proxy
// has a list for that node at all.
func (inv *inventory) held(node string, vmid int) (held, known bool) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	set, known := inv.vmids[node]
	return set[vmid], known
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
	ctx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	for _, a := range fanOut(ctx, s.nodes, "/cluster/resources", "type=vm") {
		if a.failure() == nil {
			s.inventory.record(a.node.name, a.res.data())
		}
	}
}
