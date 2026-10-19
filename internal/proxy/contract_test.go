package proxy

import (
	"context"
	"strings"
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
)

// The tests in this file drive the proxy with go-proxmox, the client CAPMOX uses
// (keep its version in go.mod at the one CAPMOX's go.mod pins), and make the calls
// CAPMOX makes in the order it makes them.

func newGoProxmox(base string) *proxmox.Client {
	return proxmox.NewClient(base+apiPrefix, proxmox.WithAPIToken("capmox@pve!capmox", "client-secret"))
}

// TestContractCreateMachineOnAnotherNode replays CAPMOX's createVM for a machine
// scheduled on pve02 from a template it found by tags: Version (client start),
// CheckID, Resources (FindVMTemplateByTags), Node.Status and Node.VirtualMachine on
// the template's node, Clone with a target, then the task status.
func TestContractCreateMachineOnAnotherNode(t *testing.T) {
	ctx := context.Background()
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /version", 200, `{"data":{"release":"9.1","version":"9.1.2","repoid":"x"}}`)
	for _, n := range []*fakeNode{a, b} {
		n.on("GET /cluster/nextid?vmid=2041", 200, `{"data":"2041"}`)
		n.on("GET /cluster/resources?type=vm", 200, `{"data":[
			{"id":"qemu/9000","type":"qemu","vmid":9000,"node":"`+n.name+`","template":1,"tags":"debian13;k0s"}]}`)
		n.on("GET /nodes/"+n.name+"/qemu/9000/config", 200, templateConfig)
	}
	a.on("GET /nodes/pve01/status", 200, `{"data":{"memory":{"total":68719476736}}}`)
	a.on("GET /nodes/pve01/qemu/9000/status/current", 200, `{"data":{"vmid":9000,"name":"debian13","status":"stopped","template":1}}`)
	upid := "UPID:pve02:000A:000B:6710A000:qmclone:9000:proxy@pve!pve02:"
	b.on("POST /nodes/pve02/qemu/9000/clone", 200, `{"data":"`+upid+`"}`)
	b.on("GET /nodes/pve02/tasks/"+upid+"/status", 200, `{"data":{"upid":"`+upid+`","node":"pve02","status":"stopped","exitstatus":"OK","type":"qmclone"}}`)
	base := newProxy(t, a, b)
	client := newGoProxmox(base)

	if v, err := client.Version(ctx); err != nil || v.Release != "9.1" {
		t.Fatalf("Version: %v %v", v, err)
	}
	cluster, err := client.Cluster(ctx)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if free, err := cluster.CheckID(ctx, 2041); err != nil || !free {
		t.Fatalf("CheckID: free=%v err=%v", free, err)
	}
	resources, err := cluster.Resources(ctx, "vm")
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}
	var templates []*proxmox.ClusterResource
	for _, r := range resources {
		if r.Template == 1 && strings.Contains(r.Tags, "k0s") {
			templates = append(templates, r)
		}
	}
	if len(templates) != 1 || templates[0].Node != "pve01" {
		t.Fatalf("templates by tag: %+v, want exactly one on pve01", templates)
	}

	node := (&proxmox.Node{}).New(client, templates[0].Node)
	if err := node.Status(ctx); err != nil {
		t.Fatalf("Node.Status: %v", err)
	}
	tpl, err := node.VirtualMachine(ctx, 9000)
	if err != nil {
		t.Fatalf("Node.VirtualMachine: %v", err)
	}
	newID, task, err := tpl.Clone(ctx, &proxmox.VirtualMachineCloneOptions{
		NewID: 2041, Name: "m2", Full: true, Pool: "capi", Target: "pve02",
	})
	if err != nil || newID != 2041 {
		t.Fatalf("Clone: id=%d err=%v", newID, err)
	}
	if task.Node != "pve02" {
		t.Fatalf("clone task on %q, want pve02", task.Node)
	}

	polled := proxmox.NewTask(task.UPID, client)
	if err := polled.Ping(ctx); err != nil {
		t.Fatalf("task Ping: %v", err)
	}
	if !polled.IsSuccessful {
		t.Errorf("task not successful: %+v", polled)
	}
}

// TestContractCheckIDTaken checks that go-proxmox reads a VMID taken on one node, or
// unknown because a node is down, as taken and not as an error.
func TestContractCheckIDTaken(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /version", 200, `{"data":{"release":"9.1"}}`)
	a.on("GET /cluster/nextid?vmid=2040", 200, `{"data":"2040"}`)
	b.on("GET /cluster/nextid?vmid=2040", 400, taken)
	a.on("GET /cluster/nextid?vmid=2042", 200, `{"data":"2042"}`)
	base := newProxy(t, a, b)
	cluster, err := newGoProxmox(base).Cluster(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if free, err := cluster.CheckID(context.Background(), 2040); err != nil || free {
		t.Errorf("taken on pve02: free=%v err=%v, want taken", free, err)
	}
	b.srv.Close()
	if free, err := cluster.CheckID(context.Background(), 2042); err != nil || free {
		t.Errorf("pve02 down: free=%v err=%v, want taken", free, err)
	}
}

// TestContractNodeErrorsAreErrors checks that a failure on a node reaches go-proxmox
// as an error, not as an empty success.
func TestContractNodeErrorsAreErrors(t *testing.T) {
	a, b := newFakeNode(t, "pve01"), newFakeNode(t, "pve02")
	a.on("GET /cluster/resources?type=vm", 200, `{"data":[]}`)
	base := newProxy(t, a, b)
	client := newGoProxmox(base)
	b.srv.Close()

	cluster, err := client.Cluster(context.Background())
	if err != nil {
		t.Fatalf("cluster status with a node down: %v", err)
	}
	if _, err := cluster.Resources(context.Background(), "vm"); err == nil {
		t.Error("Resources with pve02 down: no error")
	}
	node := (&proxmox.Node{}).New(client, "pve02")
	if err := node.Status(context.Background()); err == nil {
		t.Error("Node.Status of a down node: no error")
	}
	if _, err := (&proxmox.Node{}).New(client, "pve01").VirtualMachine(context.Background(), 404); err == nil {
		t.Error("VirtualMachine the node does not have: no error")
	}
}
