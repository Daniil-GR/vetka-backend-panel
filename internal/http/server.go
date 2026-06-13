package http

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/http/handlers"
	"vetka-backend-panel/internal/http/middleware"
	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/subscriptions"
	"vetka-backend-panel/internal/users"
	"vetka-backend-panel/web"
)

type App struct {
	Handler          http.Handler
	ExpiryReconciler *users.ExpiryReconciler
}

type dbPinger interface {
	Ping(context.Context) error
}

func NewServer(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) *App {
	appLocation := loadAppLocation(cfg.AppTimezone)
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"mask":                handlers.Mask,
		"formatTime":          func(t any) string { return formatTime(t, appLocation) },
		"formatDateTime":      func(t *time.Time) string { return formatDateTime(t, appLocation) },
		"formatDateTimeInput": func(t *time.Time) string { return formatDateTimeInput(t, appLocation) },
		"join":                strings.Join,
	}).ParseFS(web.FS, "templates/*.html"))

	nodeRepo := nodes.NewRepository(pool)
	userRepo := users.NewRepository(pool)
	nodeManager := nodes.NewManager(nodeRepo, userRepo, nodes.NewAgentClient())
	userSvc := users.NewService(userRepo)
	subSvc := subscriptions.NewService(userRepo, cfg.AppEnv == "dev", cfg.SubscriptionProfileTitle, cfg.SubscriptionUpdateIntervalHours)
	expiryReconciler := users.NewExpiryReconciler(userRepo, func(ctx context.Context, nodeID string) error {
		_, err := nodeManager.SyncNode(ctx, nodeID)
		return err
	}, logger, cfg.ExpiryReconcileInterval)
	h := handlers.New(cfg, logger, tmpl, nodeRepo, nodeManager, expiryReconciler, userRepo, userSvc, subSvc)

	r := chi.NewRouter()
	r.Get("/static/*", func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.FS(web.FS)).ServeHTTP(w, r)
	})
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/ready", readyHandler(pool, logger))
	r.Get("/sub/{token}", h.Subscription)
	r.Get("/login", h.LoginPage)
	r.Post("/login", func(w http.ResponseWriter, r *http.Request) {
		if middleware.Login(cfg, w, r) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
	})

	r.Group(func(protected chi.Router) {
		protected.Use(func(next http.Handler) http.Handler { return middleware.UIAuth(cfg, next) })
		protected.Get("/", h.Dashboard)
		protected.Get("/nodes", h.Nodes)
		protected.Get("/nodes/{id}/edit", h.EditNodePage)
		protected.Post("/nodes", h.CreateNode)
		protected.Post("/nodes/{id}", h.UpdateNode)
		protected.Post("/nodes/{id}/validate-status", h.ValidateNodeStatus)
		protected.Post("/nodes/{id}/delete", h.DeleteNode)
		protected.Post("/nodes/{id}/health", h.NodeHealth)
		protected.Post("/nodes/{id}/status", h.NodeStatus)
		protected.Post("/nodes/{id}/sync", h.SyncNode)
		protected.Post("/nodes/sync-all", h.SyncAllNodes)
		protected.Get("/users", h.Users)
		protected.Post("/users", h.CreateUser)
		protected.Get("/users/{id}", h.UserDetail)
		protected.Post("/users/{id}", h.UpdateUser)
		protected.Post("/users/{id}/delete", h.DeleteUser)
		protected.Post("/users/{id}/enable", h.EnableUser)
		protected.Post("/users/{id}/disable", h.DisableUser)
		protected.Post("/users/{id}/assign-node", h.AssignNode)
		protected.Post("/users/{id}/nodes/{accessID}/enable", h.EnableUserNodeAccess)
		protected.Post("/users/{id}/nodes/{accessID}/disable", h.DisableUserNodeAccess)
		protected.Post("/users/{id}/unassign-node", h.UnassignNode)
		protected.Post("/users/{id}/sync", h.SyncUserNodes)
		protected.Post("/users/reconcile-expired", h.ReconcileExpiredUsers)
	})

	r.Group(func(api chi.Router) {
		api.Use(func(next http.Handler) http.Handler { return middleware.APIAuth(cfg, next) })
		api.Get("/api/nodes", h.APIListNodes)
		api.Post("/api/nodes", h.APICreateNode)
		api.Get("/api/nodes/{id}", h.APIGetNode)
		api.Patch("/api/nodes/{id}", h.APIUpdateNode)
		api.Post("/api/nodes/{id}/health", h.APINodeHealth)
		api.Post("/api/nodes/{id}/status", h.APINodeStatus)
		api.Post("/api/nodes/{id}/sync", h.APISyncNode)
		api.Post("/api/nodes/sync-all", h.APISyncAllNodes)
		api.Post("/api/users", h.APICreateUser)
		api.Get("/api/users/{id}", h.APIGetUser)
		api.Patch("/api/users/{id}", h.APIUpdateUser)
		api.Post("/api/users/{id}/enable", h.APIEnableUser)
		api.Post("/api/users/{id}/disable", h.APIDisableUser)
		api.Post("/api/users/{id}/assign-node", h.APIAssignNode)
		api.Post("/api/users/{id}/unassign-node", h.APIUnassignNode)
		api.Post("/api/users/{id}/sync", h.APISyncUser)
		api.Get("/api/users/{id}/subscription", h.APIUserSubscription)
		api.Post("/api/users/reconcile-expired", h.APIReconcileExpiredUsers)
	})

	return &App{
		Handler:          requestLog(logger, r),
		ExpiryReconciler: expiryReconciler,
	}
}

func readyHandler(pinger dbPinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pinger.Ping(r.Context()); err != nil {
			if logger != nil {
				logger.Warn("database not ready", "error", sanitizeReadyError(err))
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable\n"))
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	}
}

func sanitizeReadyError(err error) string {
	if err == nil {
		return ""
	}
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.Canceled):
		return "context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.As(err, &timeout) && timeout.Timeout():
		return "timeout"
	default:
		return "ping failed"
	}
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func formatTime(t any, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	switch value := t.(type) {
	case time.Time:
		return value.In(loc).Format("2006-01-02 15:04 MST")
	case *time.Time:
		if value == nil {
			return ""
		}
		return value.In(loc).Format("2006-01-02 15:04 MST")
	default:
		return ""
	}
}

func formatDateTimeInput(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02T15:04")
}

func formatDateTime(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "unlimited"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

func loadAppLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
