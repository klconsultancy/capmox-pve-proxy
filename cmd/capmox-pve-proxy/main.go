// Command capmox-pve-proxy presents several standalone Proxmox VE nodes to
// CAPMOX as one Proxmox cluster. It is a skeleton: the proxy is not
// implemented yet.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "capmox-pve-proxy %s: not implemented yet\n", version)
	os.Exit(1)
}
