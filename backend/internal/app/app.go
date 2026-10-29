// Package app assembles the modular monolith: one identity service, one HTTP
// surface, one worker. Every domain package is wired here and nowhere else.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"rka/internal/admin"
	"rka/internal/ai"
	"rka/internal/audit"
	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/content"
	"rka/internal/evidence"
	"rka/internal/httpx"
	"rka/internal/ops"
	"rka/internal/practice"
	"rka/internal/store"
	"rka/internal/teaching"
	"rka/internal/worker"
	"rka/internal/writing"
)

type App struct {
	Config  *config.Config
	DB      *store.DB
	Log     *slog.Logger
	Handler http.Handler
	Worker  *worker.Worker

	Auth     *auth.Service
	Content  *content.Service
	Practice *practice.Service
	Writing  *writing.Service
	Evidence *evidence.Service
	Teaching *teaching.Service
	AI       *ai.Service
	Ops      *ops.Service
	Admin    *admin.Service
}

// New builds every service and the HTTP handler. It does not start listening.
func New(ctx context.Context, cfg *config.Config, db *store.DB, log *slog.Logger) (*App, error) {
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	recorder := audit.New(db, log)
	authSvc := auth.New(cfg, db, log, recorder)
	contentSvc := content.New(content.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc})
	teachingSvc := teaching.New(teaching.Deps{Config: cfg, DB: db, Log: log})
	aiSvc := ai.New(ai.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, Teaching: teachingSvc})
	practiceSvc := practice.New(practice.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, Content: contentSvc})
	writingSvc := writing.New(writing.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, AI: aiSvc})
	evidenceSvc := evidence.New(evidence.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc})
	opsSvc := ops.New(ops.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, Audit: recorder})
	adminSvc := admin.New(admin.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, AI: aiSvc, Ops: opsSvc, Teaching: teachingSvc})
	workerSvc := worker.New(cfg, log, aiSvc)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", readiness(cfg, db))
	authSvc.Register(mux)
	contentSvc.Register(mux)
	teachingSvc.RegisterKnowledge(mux, authSvc)
	practiceSvc.Register(mux)
	writingSvc.Register(mux)
	evidenceSvc.Register(mux)
	aiSvc.Register(mux)
	opsSvc.Register(mux)
	adminSvc.Register(mux)
	// License notices stay downloadable, but the content snapshot itself
	// (including reference answers) is never exposed as static web content.
	mux.HandleFunc("GET /content/licenses/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			httpx.Fail(w, r, http.StatusBadRequest, "bad_path", "路径不合法。")
			return
		}
		http.ServeFile(w, r, filepath.Join(cfg.ContentSnapshot, "licenses", name))
	})
	// Unknown API routes must return the same JSON envelope as known ones,
	// never the SPA index.html or a plain-text 404.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, http.StatusNotFound, "not_found", "接口不存在。")
	})
	mux.Handle("/", httpx.Static(cfg.StaticDir))

	handler := httpx.Chain(mux,
		httpx.Recover(log),
		httpx.RequestID,
		httpx.AccessLog(log),
		httpx.SecurityHeaders,
		httpx.SameOrigin,
		authSvc.Middleware,
	)

	return &App{
		Config: cfg, DB: db, Log: log,
		Handler: handler, Worker: workerSvc,
		Auth: authSvc, Content: contentSvc, Practice: practiceSvc, Writing: writingSvc,
		Evidence: evidenceSvc, Teaching: teachingSvc, AI: aiSvc, Ops: opsSvc, Admin: adminSvc,
	}, nil
}

// readiness reports whether the platform can serve learning traffic. Process
// liveness, database connectivity, schema compatibility and core content are
// separate signals so an operator can tell them apart.
func readiness(cfg *config.Config, db *store.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		checks := map[string]any{}
		ready := true
		if err := db.Healthy(ctx); err != nil {
			checks["database"] = "unavailable"
			ready = false
		} else {
			checks["database"] = "ok"
		}
		binaryVersion, err := store.BinarySchemaVersion()
		if err != nil {
			checks["schema"] = "unknown"
			ready = false
		} else if current, err := db.SchemaVersion(ctx); err != nil {
			checks["schema"] = "unavailable"
			ready = false
		} else if current != binaryVersion {
			checks["schema"] = fmt.Sprintf("mismatch: database=%d binary=%d", current, binaryVersion)
			ready = false
		} else {
			checks["schema"] = fmt.Sprintf("v%d", current)
		}
		var materials, questions int
		if err := db.Pool.QueryRow(ctx, `select count(*) from materials where review_status = 'published'`).Scan(&materials); err != nil {
			ready = false
			checks["content"] = "unavailable"
		} else if err := db.Pool.QueryRow(ctx, `select count(*) from questions where review_status = 'published'`).Scan(&questions); err != nil {
			ready = false
			checks["content"] = "unavailable"
		} else {
			checks["content"] = map[string]any{"materials": materials, "questions": questions}
			if materials == 0 || questions == 0 {
				ready = false
				checks["content"] = "empty: run `rka import-content`"
			}
		}
		checks["teaching"] = teachingStatus(cfg)
		status := http.StatusOK
		label := "ready"
		if !ready {
			status = http.StatusServiceUnavailable
			label = "not_ready"
		}
		httpx.JSON(w, r, status, map[string]any{"status": label, "checks": checks})
	}
}

func teachingStatus(cfg *config.Config) string {
	if cfg.Teaching.DeepTutorURL == "" {
		return "unconfigured"
	}
	return "configured"
}

// Run serves HTTP until the context is cancelled, optionally running the worker
// in-process for single-process development.
func (a *App) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              a.Config.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE streams stay open
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("http listening", "addr", a.Config.Addr, "data_dir", a.Config.DataDir, "static", a.Config.StaticDir)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if a.Config.Worker.Inline && a.Config.Worker.Enabled {
		go func() {
			if err := a.Worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.Log.Error("inline worker stopped", "error", err)
			}
		}()
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		a.Log.Info("shutting down")
		return server.Shutdown(shutdownCtx)
	}
}
