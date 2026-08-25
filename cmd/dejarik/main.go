// Dejarik — the arcade's panel: what can I play, wake it, and my devices.
//
// It owns no machinery. Power belongs to Le Veilleur, the firewall to Le
// Videur, identity to the gateway, pairing to Sunshine, saves to the tank.
// This is the face: it knows who is asking, turns that into requests to
// whoever owns the thing, and turns their state back into words.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/store"
	"github.com/tomblancdev/dejarik/internal/web"
)

// set by -ldflags "-X main.version=..."
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "dejarik")
	slog.SetDefault(log)

	path := os.Getenv("DEJARIK_CONFIG")
	if path == "" {
		path = "/etc/dejarik/config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Error("config", "path", path, "err", err)
		os.Exit(1)
	}
	if cfg.Timezone != "" {
		if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
			time.Local = loc
		}
	}
	if d := os.Getenv("DEJARIK_DATA_DIR"); d != "" {
		cfg.DataDir = d
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	au, err := auth.New(cfg.Auth)
	if err != nil {
		log.Error("auth", "err", err)
		os.Exit(1)
	}
	svc, err := arcade.New(cfg, st, log)
	if err != nil {
		log.Error("service", "err", err)
		os.Exit(1)
	}
	srv, err := web.New(cfg, svc, au, version, log)
	if err != nil {
		log.Error("web", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sd)
	}()

	log.Info("listening", "addr", cfg.Listen, "version", version, "projects", len(cfg.Projects))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http", "err", err)
		os.Exit(1)
	}
	log.Info("bye")
}
