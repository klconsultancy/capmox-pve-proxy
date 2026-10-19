// Package config loads the proxy's configuration: the nodes it fronts, the token
// CAPMOX authenticates with, and how it serves.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the proxy's configuration file. Secrets are never in it: each is read
// from its own file, such as a mounted Kubernetes Secret.
type Config struct {
	// Listen is the address to serve on, for example ":8006".
	Listen string `yaml:"listen"`
	// ClusterName is the name /cluster/status reports for the nodes together.
	ClusterName string `yaml:"clusterName"`
	// TLS is the certificate the proxy serves. Required unless InsecureHTTP is set.
	TLS *TLS `yaml:"tls"`
	// InsecureHTTP serves plain HTTP. For tests only: the token CAPMOX sends
	// would cross the network in the clear.
	InsecureHTTP bool `yaml:"insecureHTTP"`
	// Client is the token CAPMOX authenticates to the proxy with.
	Client Token `yaml:"client"`
	// Nodes are the standalone Proxmox VE nodes, in order of preference: a
	// template on several nodes is reported on the first of them.
	Nodes []Node `yaml:"nodes"`
}

// TLS is a certificate and its key, as PEM files.
type TLS struct {
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

// Token is a Proxmox API token: its ID (user@realm!name) and the file holding its secret.
type Token struct {
	ID         string `yaml:"tokenID"`
	SecretFile string `yaml:"secretFile"`
}

// Node is one standalone Proxmox VE node.
type Node struct {
	// Name is the node's own name, as Proxmox reports it (`hostname`).
	Name string `yaml:"name"`
	// URL is the node's API base URL, for example https://10.0.0.2:8006.
	URL string `yaml:"url"`
	// Token is the token the proxy uses on this node.
	Token Token `yaml:",inline"`
	// CAFile is the PEM file of the CA that signed the node's certificate, such as
	// the node's /etc/pve/pve-root-ca.pem. Empty uses the system's CAs.
	CAFile string `yaml:"caFile"`
}

// Load reads and validates the configuration file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate checks that the configuration is complete and consistent.
func (c *Config) Validate() error {
	var errs []error
	if c.Listen == "" {
		errs = append(errs, errors.New("listen is required"))
	}
	if c.ClusterName == "" {
		errs = append(errs, errors.New("clusterName is required"))
	}
	if c.TLS == nil && !c.InsecureHTTP {
		errs = append(errs, errors.New("tls is required unless insecureHTTP is set"))
	}
	if c.TLS != nil && (c.TLS.CertFile == "" || c.TLS.KeyFile == "") {
		errs = append(errs, errors.New("tls needs certFile and keyFile"))
	}
	errs = append(errs, validateToken("client", c.Client))
	if len(c.Nodes) == 0 {
		errs = append(errs, errors.New("at least one node is required"))
	}
	seen := map[string]bool{}
	for i, n := range c.Nodes {
		where := fmt.Sprintf("nodes[%d]", i)
		if n.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		} else if seen[n.Name] {
			errs = append(errs, fmt.Errorf("%s: node %q is listed twice", where, n.Name))
		}
		seen[n.Name] = true
		if u, err := url.Parse(n.URL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: url %q is not an absolute URL", where, n.URL))
		}
		errs = append(errs, validateToken(where, n.Token))
	}
	return errors.Join(errs...)
}

func validateToken(where string, t Token) error {
	var errs []error
	if !strings.Contains(t.ID, "!") {
		errs = append(errs, fmt.Errorf("%s: tokenID %q is not of the form user@realm!name", where, t.ID))
	}
	if t.SecretFile == "" {
		errs = append(errs, fmt.Errorf("%s: secretFile is required", where))
	}
	return errors.Join(errs...)
}

// ReadSecret returns the token's secret, read from its file without surrounding whitespace.
func (t Token) ReadSecret() (string, error) {
	data, err := os.ReadFile(t.SecretFile)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("%s is empty", t.SecretFile)
	}
	return secret, nil
}
