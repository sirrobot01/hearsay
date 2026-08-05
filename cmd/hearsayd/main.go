// Command hearsayd is the standalone Hearsay daemon: it keeps the
// local store, participates in the network, and serves the HTTP query
// API for stacks that do not embed the library in-process.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/transport"
	"github.com/sirrobot01/hearsay/usenet"
)

func main() {
	configPath := flag.String("config", "", "path to config file (optional; defaults are fully functional)")
	listen := flag.String("listen", "", "HTTP listen address (overrides config)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
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
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer h.Close()
	log.Info("identity", "pubkey", "ed25519:"+hex.EncodeToString(h.Identity()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Hearsay.Enabled {
		var interval time.Duration
		if cfg.Hearsay.Interval != "" {
			interval, err = time.ParseDuration(cfg.Hearsay.Interval)
			if err != nil {
				log.Error("config", "err", err)
				os.Exit(1)
			}
		}
		node, err := transport.Listen(filepath.Join(cfg.Dir, "swarm"), cfg.Hearsay.Port)
		if err != nil {
			log.Error("transport", "err", err)
			os.Exit(1)
		}
		defer node.Close()
		for _, d := range domains {
			node.Track(d.Namespace())
			for _, f := range cfg.Hearsay.Follow {
				key, err := f.Key()
				if err != nil {
					log.Error("config", "err", err)
					os.Exit(1)
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
		log.Error("http", "err", err)
		os.Exit(1)
	}
}
