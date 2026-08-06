package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
		Args: cobra.NoArgs,
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
		domains = append(domains, debrid.New(p))
	}
	for _, b := range cfg.Adapters.Usenet {
		domains = append(domains, usenet.New(b))
	}
	h, err := hearsay.New(cfg.Dir, domains...)
	if err != nil {
		return err
	}
	defer h.Close()
	log.Info("identity", "pubkey", "ed25519:"+hex.EncodeToString(h.Identity()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Hearsay.Enabled {
		var interval time.Duration
		if cfg.Hearsay.Interval != "" {
			if interval, err = time.ParseDuration(cfg.Hearsay.Interval); err != nil {
				return err
			}
		}
		node, err := transport.Listen(filepath.Join(cfg.Dir, "swarm"), cfg.Hearsay.Port, cfg.Hearsay.GossipPort)
		if err != nil {
			return err
		}
		defer node.Close()
		for _, d := range domains {
			node.Track(d.Namespace())
			for _, f := range cfg.Hearsay.Follow {
				key, err := f.Key()
				if err != nil {
					return err
				}
				node.AddFeed(d.Namespace(), key)
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

	srv := &http.Server{Addr: cfg.Listen, Handler: (&server{h: h}).mux()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", cfg.Listen)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("http: %w", err)
	}
	return nil
}
