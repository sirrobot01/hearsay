package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/transport"
	"github.com/sirrobot01/hearsay/usenet"
)

func serveCmd() *cobra.Command {
	var configPath, listen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon: local store, network sync, HTTP query API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("config") {
				if _, err := os.Stat(configPath); err != nil {
					return err
				}
			}
			return serve(configPath, listen)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath(), "path to config file (missing file = defaults)")
	cmd.Flags().StringVar(&listen, "listen", "", "HTTP listen address (overrides config)")
	return cmd
}

func serve(configPath, listen string) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}

	var domains []hearsay.Domain
	for _, p := range cfg.Adapters.Debrid {
		domains = append(domains, debrid.New(p), debrid.NewUncached(p))
	}
	for _, b := range cfg.Adapters.Usenet {
		domains = append(domains, usenet.New(b))
	}
	h, err := hearsay.New(cfg.Dir, domains...)
	if err != nil {
		return err
	}
	defer h.Close()
	self := hex.EncodeToString(h.Identity())
	log.Info("identity", "pubkey", "ed25519:"+self)

	token, err := loadToken(cfg.Dir)
	if err != nil {
		return err
	}
	log.Info("api token", "path", filepath.Join(cfg.Dir, tokenFile))

	// A non-empty follow list is an allowlist, not a set of extra seeds:
	// resolve it before the node starts so discovery never runs.
	var follow []string
	for _, f := range cfg.Hearsay.Follow {
		key, err := f.Key()
		if err != nil {
			return err
		}
		follow = append(follow, key)
	}
	// Retained generations answer queries on their own, whether or not
	// the network is running, so narrow the store before anything reads
	// it: an allowlist has to drop what an earlier run discovered.
	if len(follow) > 0 {
		for _, d := range domains {
			ns := d.Namespace()
			for _, feed := range h.Feeds(ns) {
				if feed == self || slices.Contains(follow, feed) {
					continue
				}
				if err := h.Forget(ns, feed); err != nil {
					return err
				}
				log.Info("dropped a retained feed outside the follow list", "ns", ns, "feed", feed[:8])
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var node *transport.Node
	if cfg.Hearsay.Enabled {
		var interval time.Duration
		if cfg.Hearsay.Interval != "" {
			if interval, err = time.ParseDuration(cfg.Hearsay.Interval); err != nil {
				return err
			}
		}
		node, err = transport.ListenWithConfig(transport.NodeConfig{
			Dir:        filepath.Join(cfg.Dir, "swarm"),
			Port:       cfg.Hearsay.Port,
			GossipPort: cfg.Hearsay.GossipPort,
			MaxStorage: cfg.Hearsay.MaxStorage,
		})
		if err != nil {
			return err
		}
		defer node.Close()
		if len(follow) > 0 {
			node.Allow(append(slices.Clone(follow), self)...)
			log.Info("following an explicit publisher set; discovery and gossip cannot add to it", "publishers", len(follow))
		}
		for _, d := range domains {
			ns := d.Namespace()
			node.Track(ns)
			for _, key := range follow {
				node.AddFeed(ns, key)
			}
		}
		syncer := &transport.Syncer{
			Engine:   h,
			Node:     node,
			Publish:  cfg.Hearsay.Publish,
			Interval: interval,
			Log:      log,
		}
		go syncer.Run(ctx)
	} else {
		log.Info("network participation disabled; local ground truth only")
	}

	api := &server{h: h, node: node, token: token, hosts: cfg.AllowedHosts}
	srv := &http.Server{Addr: cfg.Listen, Handler: api.mux()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", cfg.Listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http: %w", err)
	}
	return nil
}
