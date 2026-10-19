// Command capmox-pve-proxy presents several standalone Proxmox VE nodes to CAPMOX
// as one Proxmox cluster.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/klconsultancy/capmox-pve-proxy/internal/config"
	"github.com/klconsultancy/capmox-pve-proxy/internal/proxy"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "capmox-pve-proxy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/capmox-pve-proxy/config.yaml", "configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	refresh := flag.Duration("refresh", 30*time.Second, "how often to read each node's guests")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	handler, err := proxy.New(cfg, log)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// What each node holds, for handing out VMIDs while a node is down.
	go handler.Refresh(ctx, *refresh)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	nodes := make([]string, len(cfg.Nodes))
	for i, n := range cfg.Nodes {
		nodes[i] = n.Name
	}
	log.Info("serving", "version", version, "listen", cfg.Listen, "cluster", cfg.ClusterName, "nodes", nodes)
	if cfg.InsecureHTTP {
		log.Warn("serving plain HTTP: the client token crosses the network in the clear")
		err = srv.ListenAndServe()
	} else {
		err = srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
