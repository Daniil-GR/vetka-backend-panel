package middleware

import (
	"net/http"
	"strings"
	"time"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/security"
)

func UIAuth(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/sub/") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("vetka_admin")
		if err == nil && security.ConstantTimeEqual(cookie.Value, cfg.AdminAPIToken) {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

func APIAuth(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !security.ConstantTimeEqual(token, cfg.AdminAPIToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Login(cfg config.Config, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if !security.ConstantTimeEqual(r.FormValue("username"), cfg.AdminUsername) || !security.ConstantTimeEqual(r.FormValue("password"), cfg.AdminPassword) {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vetka_admin",
		Value:    cfg.AdminAPIToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	return true
}
