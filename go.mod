module github.com/klconsultancy/capmox-pve-proxy

go 1.27

require (
	// Test only: the contract tests use the go-proxmox version CAPMOX v0.9.1 pins.
	github.com/luthermonson/go-proxmox v0.8.2
	go.yaml.in/yaml/v3 v3.0.5
)

require (
	github.com/buger/goterm v1.0.4 // indirect
	github.com/diskfs/go-diskfs v1.9.3 // indirect
	github.com/djherbis/times v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect; raised from go-proxmox's v1.4.2 for GHSA-w67g-5rqw-f597
	github.com/jinzhu/copier v0.3.4 // indirect
	github.com/magefile/mage v1.14.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
