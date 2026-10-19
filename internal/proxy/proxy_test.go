package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRejectsWrongToken(t *testing.T) {
	a := newFakeNode(t, "pve01")
	base := newProxy(t, a)

	for _, auth := range []string{"", "PVEAPIToken=capmox@pve!capmox=wrong", a.auth()} {
		req, _ := http.NewRequest(http.MethodGet, base+apiPrefix+"/version", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, res.StatusCode)
		}
	}
	if n := len(a.got()); n != 0 {
		t.Errorf("node got %d requests, want none", n)
	}
}

func TestRefusesUnimplementedEndpoints(t *testing.T) {
	a := newFakeNode(t, "pve01")
	base := newProxy(t, a)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/nodes/pve01/qemu/101/rrddata"},
		{http.MethodPost, "/nodes/pve01/qemu/101/status/shutdown"},
		{http.MethodPut, "/nodes/pve01/qemu/101/config"}, // only GET and POST are used
		{http.MethodGet, "/access/users"},
	} {
		status, body := call(t, base, tc.method, tc.path, "", "")
		if status != http.StatusNotImplemented || !strings.Contains(body, "does not implement") {
			t.Errorf("%s %s: %d %s, want 501", tc.method, tc.path, status, body)
		}
	}
	if n := len(a.got()); n != 0 {
		t.Errorf("node got %d requests, want none", n)
	}
}

func TestForwardsToTheNodeInThePath(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	b.on("GET /nodes/pve02/qemu/2031/agent/exec-status?pid=7", 200, `{"data":{"exited":1}}`)
	b.on("DELETE /nodes/pve02/storage/local/content/local:iso/user-data-2031.iso", 200, `{"data":"UPID:pve02:1:2:3:imgdel:x:root@pam:"}`)
	base := newProxy(t, a, b)

	status, body := call(t, base, http.MethodGet, "/nodes/pve02/qemu/2031/agent/exec-status?pid=7", "", "")
	if status != 200 || !strings.Contains(body, `"exited":1`) {
		t.Fatalf("exec-status: %d %s", status, body)
	}
	status, _ = call(t, base, http.MethodDelete, "/nodes/pve02/storage/local/content/local:iso/user-data-2031.iso", "", "")
	if status != 200 {
		t.Fatalf("delete ISO: %d", status)
	}

	got := b.got()
	if len(got) != 2 || len(a.got()) != 0 {
		t.Fatalf("pve01 got %d, pve02 got %d requests; want 0 and 2", len(a.got()), len(got))
	}
	if got[0].auth != b.auth() {
		t.Errorf("node got Authorization %q, want its own token", got[0].auth)
	}
	if got[0].query != "pid=7" {
		t.Errorf("query %q, want pid=7", got[0].query)
	}
}

func TestForwardsBodies(t *testing.T) {
	a := newFakeNode(t, "pve01")
	a.on("POST /nodes/pve01/storage/local/upload", 200, `{"data":"UPID:pve01:1:2:3:imgcopy::root@pam:"}`)
	base := newProxy(t, a)

	multipart := "--b\r\nContent-Disposition: form-data; name=\"content\"\r\n\r\niso\r\n--b--\r\n"
	status, _ := call(t, base, http.MethodPost, "/nodes/pve01/storage/local/upload", "multipart/form-data; boundary=b", multipart)
	if status != 200 {
		t.Fatalf("upload: %d", status)
	}
	got := a.got()[0]
	if got.body != multipart || got.contentType != "multipart/form-data; boundary=b" {
		t.Errorf("node got %q (%s), want the upload unchanged", got.body, got.contentType)
	}
}

func TestUnknownNode(t *testing.T) {
	base := newProxy(t, newFakeNode(t, "pve01"))
	status, body := call(t, base, http.MethodGet, "/nodes/pve09/status", "", "")
	if status != http.StatusInternalServerError || !strings.Contains(body, "no such node 'pve09'") {
		t.Errorf("%d %s, want 500 no such node", status, body)
	}
}

func TestTasksGoToTheNodeInTheirUPID(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	upid := "UPID:pve02:0001:0002:6710A000:qmclone:2031:proxy@pve!pve02:"
	b.on("GET /nodes/pve02/tasks/"+upid+"/status", 200, `{"data":{"status":"stopped","exitstatus":"OK"}}`)
	base := newProxy(t, a, b)

	// The path names pve01, but the task ran on pve02.
	status, body := call(t, base, http.MethodGet, "/nodes/pve01/tasks/"+upid+"/status", "", "")
	if status != 200 || !strings.Contains(body, "stopped") {
		t.Fatalf("%d %s", status, body)
	}
	if len(a.got()) != 0 || len(b.got()) != 1 {
		t.Errorf("pve01 got %d, pve02 got %d requests", len(a.got()), len(b.got()))
	}
}

func TestNodeErrorsBecomeErrorsGoProxmoxSees(t *testing.T) {
	a := newFakeNode(t, "pve01")
	a.on("GET /nodes/pve01/qemu/101/config", 595, `{"data":null}`)
	// No response set for status/current: the fake answers 404.
	base := newProxy(t, a)

	for _, path := range []string{"/nodes/pve01/qemu/101/config", "/nodes/pve01/qemu/101/status/current"} {
		if status, _ := call(t, base, http.MethodGet, path, "", ""); status != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", path, status)
		}
	}
}

func TestUnreachableNode(t *testing.T) {
	a := newFakeNode(t, "pve01")
	base := newProxy(t, a)
	a.srv.Close()

	status, body := call(t, base, http.MethodGet, "/nodes/pve01/status", "", "")
	if status != http.StatusInternalServerError || !strings.Contains(body, "unreachable") {
		t.Errorf("%d %s, want 500 unreachable", status, body)
	}
}

func TestClusterStatus(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /version", 200, `{"data":{"release":"9.1"}}`)
	// pve02 does not answer /version: it is reported offline.
	base := newProxy(t, a, b)

	status, body := call(t, base, http.MethodGet, "/cluster/status", "", "")
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	var res struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 3 || res.Data[0]["type"] != "cluster" || res.Data[0]["nodes"] != float64(2) {
		t.Fatalf("unexpected status %s", body)
	}
	if res.Data[1]["name"] != "pve01" || res.Data[1]["online"] != float64(1) {
		t.Errorf("pve01: %v", res.Data[1])
	}
	if res.Data[2]["name"] != "pve02" || res.Data[2]["online"] != float64(0) {
		t.Errorf("pve02: %v", res.Data[2])
	}
}

func TestVersionFromTheFirstNodeThatAnswers(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	b.on("GET /version", 200, `{"data":{"release":"9.1","version":"9.1.2"}}`)
	base := newProxy(t, a, b)

	status, body := call(t, base, http.MethodGet, "/version", "", "")
	if status != 200 || !strings.Contains(body, "9.1.2") {
		t.Errorf("%d %s", status, body)
	}
}

func TestClusterResourcesMerged(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /cluster/resources?type=vm", 200, `{"data":[
		{"id":"qemu/9000","vmid":9000,"node":"pve01","template":1,"tags":"debian13;k0s"},
		{"id":"qemu/2015","vmid":2015,"node":"pve01","template":0}]}`)
	b.on("GET /cluster/resources?type=vm", 200, `{"data":[
		{"id":"qemu/9000","vmid":9000,"node":"pve02","template":1,"tags":"debian13;k0s"},
		{"id":"qemu/2031","vmid":2031,"node":"pve02","template":0}]}`)
	base := newProxy(t, a, b)

	status, body := call(t, base, http.MethodGet, "/cluster/resources?type=vm", "", "")
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	var res struct {
		Data []struct {
			VMID float64 `json:"vmid"`
			Node string  `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	got := map[float64]string{}
	for _, r := range res.Data {
		if _, dup := got[r.VMID]; dup {
			t.Errorf("VMID %v reported twice", r.VMID)
		}
		got[r.VMID] = r.Node
	}
	want := map[float64]string{9000: "pve01", 2015: "pve01", 2031: "pve02"}
	for vmid, node := range want {
		if got[vmid] != node {
			t.Errorf("VMID %v on %q, want %q", vmid, got[vmid], node)
		}
	}
}

func TestClusterResourcesFailWhenANodeDoesNotAnswer(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /cluster/resources?type=vm", 200, `{"data":[]}`)
	base := newProxy(t, a, b)
	b.srv.Close()

	if status, _ := call(t, base, http.MethodGet, "/cluster/resources?type=vm", "", ""); status != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", status)
	}
}

const taken = `{"data":null,"errors":{"vmid":"VM 2040 already exists"}}`

func TestCheckID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pve01    canned
		pve02    canned
		closeB   bool
		wantFree bool
	}{
		{name: "free on both", pve01: canned{200, `{"data":"2040"}`}, pve02: canned{200, `{"data":"2040"}`}, wantFree: true},
		{name: "taken on one", pve01: canned{200, `{"data":"2040"}`}, pve02: canned{400, taken}},
		{name: "a node does not answer", pve01: canned{200, `{"data":"2040"}`}, closeB: true},
		{name: "a node errors", pve01: canned{200, `{"data":"2040"}`}, pve02: canned{500, `{"data":null}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
			a.on("GET /cluster/nextid?vmid=2040", tc.pve01.status, tc.pve01.body)
			if tc.pve02.status != 0 {
				b.on("GET /cluster/nextid?vmid=2040", tc.pve02.status, tc.pve02.body)
			}
			base := newProxy(t, a, b)
			if tc.closeB {
				b.srv.Close()
			}

			status, body := call(t, base, http.MethodGet, "/cluster/nextid?vmid=2040", "", "")
			if tc.wantFree {
				if status != 200 || !strings.Contains(body, `"2040"`) {
					t.Errorf("%d %s, want free", status, body)
				}
				return
			}
			// go-proxmox's CheckID recognises a taken VMID by exactly this.
			if status != http.StatusBadRequest || !strings.Contains(body, "VM 2040 already exists") {
				t.Errorf("%d %s, want taken", status, body)
			}
		})
	}
}

func TestNextIDIsFreeOnEveryNode(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /cluster/nextid", 200, `{"data":"100"}`)
	b.on("GET /cluster/nextid", 200, `{"data":"104"}`)
	for _, n := range []*fakeNode{a, b} {
		for _, id := range []string{"104", "105", "106"} {
			n.on("GET /cluster/nextid?vmid="+id, 200, `{"data":"`+id+`"}`)
		}
	}
	a.on("GET /cluster/nextid?vmid=104", 400, strings.ReplaceAll(taken, "2040", "104"))
	b.on("GET /cluster/nextid?vmid=105", 400, strings.ReplaceAll(taken, "2040", "105"))
	base := newProxy(t, a, b)

	status, body := call(t, base, http.MethodGet, "/cluster/nextid", "", "")
	if status != 200 || !strings.Contains(body, `"106"`) {
		t.Errorf("%d %s, want 106", status, body)
	}
}

const templateConfig = `{"data":{"name":"debian13","template":1}}`

func TestCloneOnTheSameNodeIsForwarded(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("POST /nodes/pve01/qemu/9000/clone", 200, `{"data":"UPID:pve01:1:2:3:qmclone:9000:proxy@pve!pve01:"}`)
	base := newProxy(t, a, b)

	in := `{"newid":2040,"name":"m1","full":1,"target":"pve01"}`
	status, _ := call(t, base, http.MethodPost, "/nodes/pve01/qemu/9000/clone", "application/json", in)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if got := a.got(); len(got) != 1 || got[0].body != in {
		t.Errorf("pve01 got %+v, want the request unchanged", got)
	}
	if len(b.got()) != 0 {
		t.Errorf("pve02 got %d requests", len(b.got()))
	}
}

func TestCloneToAnotherNodeBecomesALocalClone(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"json", "application/json", `{"newid":2041,"name":"m2","full":1,"pool":"capi","target":"pve02"}`},
		{"form", "application/x-www-form-urlencoded", "newid=2041&name=m2&full=1&pool=capi&target=pve02"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
			b.on("GET /nodes/pve02/qemu/9000/config", 200, templateConfig)
			b.on("POST /nodes/pve02/qemu/9000/clone", 200, `{"data":"UPID:pve02:1:2:3:qmclone:9000:proxy@pve!pve02:"}`)
			base := newProxy(t, a, b)

			status, body := call(t, base, http.MethodPost, "/nodes/pve01/qemu/9000/clone", tc.contentType, tc.body)
			if status != 200 || !strings.Contains(body, "UPID:pve02:") {
				t.Fatalf("%d %s", status, body)
			}
			if len(a.got()) != 0 {
				t.Errorf("pve01 got %d requests, want none", len(a.got()))
			}
			got := b.got()
			if len(got) != 2 || got[1].method != http.MethodPost {
				t.Fatalf("pve02 got %+v", got)
			}
			clone := got[1]
			if strings.Contains(clone.body, "target") {
				t.Errorf("local clone still has a target: %s", clone.body)
			}
			for _, want := range []string{"2041", "m2", "capi"} {
				if !strings.Contains(clone.body, want) {
					t.Errorf("local clone %s lost %q", clone.body, want)
				}
			}
			if clone.contentType != tc.contentType {
				t.Errorf("content type %q, want %q", clone.contentType, tc.contentType)
			}
		})
	}
}

func TestCloneRefusedWithoutTheTemplateOnTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config canned
		want   string
	}{
		{"missing", canned{500, `{"data":null}`}, "template 9000 is not on node 'pve02'"},
		{"not a template", canned{200, `{"data":{"name":"vm"}}`}, "VMID 9000 on node 'pve02' is not a template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
			b.on("GET /nodes/pve02/qemu/9000/config", tc.config.status, tc.config.body)
			base := newProxy(t, a, b)

			status, body := call(t, base, http.MethodPost, "/nodes/pve01/qemu/9000/clone",
				"application/json", `{"newid":2041,"target":"pve02"}`)
			if status != http.StatusInternalServerError || !strings.Contains(body, tc.want) {
				t.Errorf("%d %s, want 500 %q", status, body, tc.want)
			}
			for _, r := range b.got() {
				if r.method == http.MethodPost {
					t.Errorf("pve02 got a clone: %+v", r)
				}
			}
		})
	}
}

func TestCloneToUnknownNode(t *testing.T) {
	base := newProxy(t, newFakeNode(t, "pve01"))
	status, body := call(t, base, http.MethodPost, "/nodes/pve01/qemu/9000/clone",
		"application/json", `{"newid":2041,"target":"pve09"}`)
	if status != http.StatusInternalServerError || !strings.Contains(body, "no such node 'pve09'") {
		t.Errorf("%d %s", status, body)
	}
}
