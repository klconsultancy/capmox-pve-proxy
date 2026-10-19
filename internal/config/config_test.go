package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "config.yaml", `
listen: ":8006"
clusterName: homelab
tls:
  certFile: /tls/tls.crt
  keyFile: /tls/tls.key
client:
  tokenID: capmox@pve!capmox
  secretFile: /secrets/client
nodes:
  - name: pve01
    url: https://10.0.0.2:8006
    tokenID: capmox@pve!proxy
    secretFile: /secrets/pve01
    caFile: /ca/pve01.pem
  - name: pve02
    url: https://10.0.0.3:8006
    tokenID: capmox@pve!proxy
    secretFile: /secrets/pve02
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Nodes) != 2 || cfg.Nodes[0].Token.ID != "capmox@pve!proxy" || cfg.Nodes[0].CAFile != "/ca/pve01.pem" {
		t.Errorf("unexpected nodes: %+v", cfg.Nodes)
	}
}

func TestLoadRejects(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"unknown field", "listen: x\nlisten2: y\n", "listen2"},
		{"no TLS", `listen: ":1"
clusterName: c
client: {tokenID: a@pve!b, secretFile: /s}
nodes: [{name: n, url: "https://h:8006", tokenID: a@pve!b, secretFile: /s}]
`, "tls is required"},
		{"duplicate node", `listen: ":1"
clusterName: c
insecureHTTP: true
client: {tokenID: a@pve!b, secretFile: /s}
nodes:
  - {name: n, url: "https://h:8006", tokenID: a@pve!b, secretFile: /s}
  - {name: n, url: "https://i:8006", tokenID: a@pve!b, secretFile: /s}
`, "listed twice"},
		{"bad token ID", `listen: ":1"
clusterName: c
insecureHTTP: true
client: {tokenID: root, secretFile: /s}
nodes: [{name: n, url: "https://h:8006", tokenID: a@pve!b, secretFile: /s}]
`, "user@realm!name"},
		{"relative URL", `listen: ":1"
clusterName: c
insecureHTTP: true
client: {tokenID: a@pve!b, secretFile: /s}
nodes: [{name: n, url: "h:8006", tokenID: a@pve!b, secretFile: /s}]
`, "not an absolute URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, t.TempDir(), "config.yaml", tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	secret, err := Token{ID: "a@pve!b", SecretFile: write(t, dir, "s", "  abc\n")}.ReadSecret()
	if err != nil || secret != "abc" {
		t.Errorf("secret %q, err %v", secret, err)
	}
	if _, err := (Token{SecretFile: write(t, dir, "empty", "\n")}).ReadSecret(); err == nil {
		t.Error("empty secret accepted")
	}
}

func TestAuthorization(t *testing.T) {
	auth, err := Token{ID: "a@pve!b", SecretFile: write(t, t.TempDir(), "s", "abc\n")}.Authorization()
	if err != nil || auth != "PVEAPIToken=a@pve!b=abc" {
		t.Errorf("authorization %q, err %v", auth, err)
	}
}
