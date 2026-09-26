// Package app assembles kmdn's services from configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/server"
	"github.com/kmdn-app/kmdn/internal/store"
)

// App holds the running services.
type App struct {
	Config  config.Config
	Log     *slog.Logger
	DB      *store.DB
	Secrets *secrets.Store
	Jobs    *jobs.Queue
	Server  *server.Server
}

// New opens the database, applies migrations and builds the services.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	n, err := db.Migrate(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	if n > 0 {
		log.Info("applied migrations", "count", n, "dialect", db.Dialect.String())
	}
	kek, err := cfg.SecretKeyBytes()
	if err != nil {
		db.Close()
		return nil, err
	}
	sec, err := secrets.New(db, kek)
	if err != nil {
		db.Close()
		return nil, err
	}
	a := &App{
		Config:  cfg,
		Log:     log,
		DB:      db,
		Secrets: sec,
		Jobs:    jobs.New(db, jobs.Options{Logger: log}),
		Server:  server.New(server.Options{Config: cfg, Logger: log}),
	}
	a.Server.AddReadyCheck("database", func(ctx context.Context) error { return db.PingContext(ctx) })
	return a, nil
}

// Run serves HTTP and runs background workers until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() { a.Jobs.Run(ctx); close(done) }()
	err := a.Server.Run(ctx)
	<-done
	return err
}

// Close releases resources.
func (a *App) Close() error { return a.DB.Close() }
