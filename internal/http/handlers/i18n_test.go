package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vetka-backend-panel/internal/config"
)

func TestResolveLocaleDefaultsToRussian(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := ResolveLocale(req); got != LocaleRU {
		t.Fatalf("expected ru default, got %q", got)
	}
}

func TestResolveLocaleFromCookie(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		expect Locale
	}{
		{name: "ru", value: "ru", expect: LocaleRU},
		{name: "en", value: "en", expect: LocaleEN},
		{name: "unknown", value: "de", expect: LocaleRU},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: languageCookieName, Value: tc.value})
			if got := ResolveLocale(req); got != tc.expect {
				t.Fatalf("expected %q, got %q", tc.expect, got)
			}
		})
	}
}

func TestSafeReturnTo(t *testing.T) {
	h := &Handler{}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "root", input: "/", want: "/"},
		{name: "dashboard", input: "/dashboard", want: "/dashboard"},
		{name: "users", input: "/users", want: "/users"},
		{name: "user detail", input: "/users/123", want: "/users/123"},
		{name: "node detail", input: "/nodes/node-1", want: "/nodes/node-1"},
		{name: "query", input: "/nodes/node-1?tab=sync", want: "/nodes/node-1?tab=sync"},
		{name: "users query", input: "/users?status=active&sort=expires_at", want: "/users?status=active&sort=expires_at"},
		{name: "empty", input: "", want: "/"},
		{name: "space", input: " ", want: "/"},
		{name: "https", input: "https://evil.example", want: "/"},
		{name: "http", input: "http://evil.example", want: "/"},
		{name: "double slash", input: "//evil.example", want: "/"},
		{name: "slash backslash", input: "/\\evil.example", want: "/"},
		{name: "encoded backslash upper", input: "/%5Cevil.example", want: "/"},
		{name: "encoded backslash lower", input: "/%5cevil.example", want: "/"},
		{name: "leading backslashes", input: "\\\\evil.example", want: "/"},
		{name: "javascript", input: "javascript:alert(1)", want: "/"},
		{name: "relative path", input: "nodes/123", want: "/"},
		{name: "crlf", input: "/path\r\nLocation:https://evil.example", want: "/"},
		{name: "newline", input: "/path\nhttps://evil.example", want: "/"},
		{name: "nul", input: "/path\x00evil", want: "/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.safeReturnTo(tc.input); got != tc.want {
				t.Fatalf("safeReturnTo(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSetLanguageSetsCookieAndSafeRedirect(t *testing.T) {
	h := &Handler{cfg: config.Config{PanelPublicBaseURL: "https://panel.example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/ui/language", nil)
	req.Form = map[string][]string{
		"language":  {"en"},
		"return_to": {"/nodes/node-1"},
	}
	rec := httptest.NewRecorder()

	h.SetLanguage(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/nodes/node-1" {
		t.Fatalf("unexpected redirect: %q", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected language cookie")
	}
	cookie := cookies[0]
	if cookie.Name != languageCookieName || cookie.Value != "en" {
		t.Fatalf("unexpected cookie: %#v", cookie)
	}
	if !cookie.Secure {
		t.Fatal("expected secure cookie for https panel url")
	}
}

func TestSetLanguageKeepsSafeLocalQueryRedirect(t *testing.T) {
	h := &Handler{cfg: config.Config{PanelPublicBaseURL: "http://panel.example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/ui/language", nil)
	req.Form = map[string][]string{
		"language":  {"ru"},
		"return_to": {"/users?status=active"},
	}
	rec := httptest.NewRecorder()

	h.SetLanguage(rec, req)

	if got := rec.Header().Get("Location"); got != "/users?status=active" {
		t.Fatalf("unexpected redirect: %q", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value != "ru" {
		t.Fatalf("expected ru cookie, got %#v", cookies)
	}
}

func TestSetLanguageRejectsUnsafeReturnTo(t *testing.T) {
	tests := []string{
		"https://evil.example",
		"/\\evil.example",
		"/%5Cevil.example",
	}

	for _, returnTo := range tests {
		t.Run(returnTo, func(t *testing.T) {
			h := &Handler{cfg: config.Config{PanelPublicBaseURL: "http://panel.example.com"}}
			req := httptest.NewRequest(http.MethodPost, "/ui/language", nil)
			req.Form = map[string][]string{
				"language":  {"en"},
				"return_to": {returnTo},
			}
			rec := httptest.NewRecorder()

			h.SetLanguage(rec, req)

			if got := rec.Header().Get("Location"); got != "/" {
				t.Fatalf("expected safe root redirect, got %q", got)
			}
			if loc := rec.Header().Get("Location"); loc == "https://evil.example" {
				t.Fatalf("unsafe redirect leaked: %q", loc)
			}
		})
	}
}

func TestSetLanguageUnknownLanguageFallsBackToRussianCookie(t *testing.T) {
	h := &Handler{cfg: config.Config{PanelPublicBaseURL: "http://panel.example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/ui/language", nil)
	req.Form = map[string][]string{
		"language":  {"de"},
		"return_to": {"/"},
	}
	rec := httptest.NewRecorder()

	h.SetLanguage(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected language cookie")
	}
	if cookies[0].Value != "ru" {
		t.Fatalf("expected fallback ru cookie, got %q", cookies[0].Value)
	}
	if cookies[0].Value == "de" {
		t.Fatalf("must not persist unknown locale value: %#v", cookies[0])
	}
}

func TestTranslateFallsBackToRussianAndKey(t *testing.T) {
	if got := Translate(LocaleEN, "nav.dashboard"); got != "Dashboard" {
		t.Fatalf("unexpected translated value: %q", got)
	}
	if got := Translate(LocaleEN, "missing.key"); got != "missing.key" {
		t.Fatalf("unexpected key fallback: %q", got)
	}
}

func TestTranslationCatalogDoesNotContainHTML(t *testing.T) {
	for locale, entries := range translations {
		for key, value := range entries {
			if strings.Contains(value, "<") || strings.Contains(value, ">") {
				t.Fatalf("translation %s/%s must not contain HTML markup: %q", locale, key, value)
			}
		}
	}
}
