package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"vetka-backend-panel/internal/users"
)

func testHandlerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSafeUIErrorTextSanitizesAndNormalizes(t *testing.T) {
	raw := "node agent returned 500:\r\n{\"password\":\"p_secret\",\"node_secret\":\"n_secret\",\"authorization\":\"Bearer abcdef\"}"
	got := safeUIErrorText(LocaleEN, raw)
	for _, secret := range []string{"p_secret", "n_secret", "abcdef", "\r", "\n"} {
		if strings.Contains(got, secret) {
			t.Fatalf("unsafe content %q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("expected redaction markers in %q", got)
	}
}

func TestSafeUIErrorTextTruncatesUnicodeSafely(t *testing.T) {
	raw := strings.Repeat("Ошибка-длинная-строка-", 30)
	got := safeUIErrorText(LocaleRU, raw)
	if len([]rune(got)) > 302 {
		t.Fatalf("expected truncated UI error, got %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "Ошибка") {
		t.Fatalf("expected readable unicode text, got %q", got)
	}
}

func TestRedirectWithErrorFlashSanitizesLocationHeader(t *testing.T) {
	h := &Handler{logger: testHandlerLogger()}
	req := httptest.NewRequest(http.MethodPost, "/nodes/node-1/sync", nil)
	rec := httptest.NewRecorder()
	err := errors.New("node agent returned 500:\n{\"password\":\"p_secret\",\"node_secret\":\"n_secret\",\"authorization\":\"Bearer abcdef\"}")

	h.redirectWithErrorFlash(rec, req, "/nodes/node-1", "Sync failed: ", err)

	location := rec.Header().Get("Location")
	decodedLocation, decodeErr := url.QueryUnescape(location)
	if decodeErr != nil {
		t.Fatalf("decode location: %v", decodeErr)
	}
	for _, secret := range []string{"p_secret", "n_secret", "abcdef"} {
		if strings.Contains(decodedLocation, secret) {
			t.Fatalf("secret %q leaked into redirect location: %s", secret, decodedLocation)
		}
	}
	if !strings.Contains(decodedLocation, "***") {
		t.Fatalf("expected redacted marker in location: %s", decodedLocation)
	}
}

func TestSanitizeNodeSyncErrorsSanitizesEachEntry(t *testing.T) {
	errs := []string{
		`node-1: {"password":"p_secret"}`,
		`node-2: authorization: Bearer abcdef protocol_password=p_demo`,
	}
	got := sanitizeNodeSyncErrors(LocaleEN, errs)
	joined := strings.Join(got, " | ")
	for _, secret := range []string{"p_secret", "abcdef", "p_demo"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret %q leaked in %q", secret, joined)
		}
	}
	if !strings.Contains(joined, "node-1:") || !strings.Contains(joined, "node-2:") {
		t.Fatalf("expected node ids to remain visible: %q", joined)
	}
}

func TestHandleErrDoesNotExposeRawInternalError(t *testing.T) {
	h := &Handler{logger: testHandlerLogger()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users", nil)

	if !h.handleErr(rec, req, errors.New(`db failed: password=secret`)) {
		t.Fatal("expected handleErr to report handled error")
	}
	if body := rec.Body.String(); strings.Contains(body, "secret") || !strings.Contains(body, "Внутренняя ошибка сервера") {
		t.Fatalf("unexpected handleErr body: %q", body)
	}
}

func TestHandleErrLocalizesInternalErrorForRussian(t *testing.T) {
	h := &Handler{logger: testHandlerLogger()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.AddCookie(&http.Cookie{Name: languageCookieName, Value: "ru"})

	if !h.handleErr(rec, req, errors.New(`db failed: password=secret`)) {
		t.Fatal("expected handleErr to report handled error")
	}
	if body := rec.Body.String(); strings.Contains(body, "secret") || !strings.Contains(body, "Внутренняя ошибка сервера") {
		t.Fatalf("unexpected localized handleErr body: %q", body)
	}
}

func TestWriteJSONOrErrorKeepsAPIErrorContract(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONOrError(rec, http.StatusOK, nil, errors.New(`api raw error`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "api raw error") {
		t.Fatalf("expected raw API error contract to remain unchanged: %q", rec.Body.String())
	}
}

func TestRedirectWithErrorFlashProducesSafeRenderableFlash(t *testing.T) {
	h := &Handler{logger: testHandlerLogger()}
	req := httptest.NewRequest(http.MethodPost, "/users/user-1/sync", nil)
	rec := httptest.NewRecorder()
	err := errors.New("protocol_username=u_demo protocol_password=p_demo")

	h.redirectWithErrorFlash(rec, req, "/users/user-1", "Sync failed: ", err)

	location := rec.Header().Get("Location")
	u, parseErr := url.Parse(location)
	if parseErr != nil {
		t.Fatalf("parse redirect location: %v", parseErr)
	}
	flash := u.Query().Get("flash")
	if strings.Contains(flash, "u_demo") || strings.Contains(flash, "p_demo") {
		t.Fatalf("raw credentials leaked in flash: %q", flash)
	}
	if !strings.Contains(flash, "***") {
		t.Fatalf("expected redacted flash text, got %q", flash)
	}
}

func TestSafeUIErrorFallsBackToLocalizedGenericMessage(t *testing.T) {
	if got := safeUIErrorText(LocaleRU, "token"); got != "Операция не выполнена. Проверьте журналы Backend." {
		t.Fatalf("unexpected ru fallback: %q", got)
	}
	if got := safeUIErrorText(LocaleEN, "token"); got != "Operation failed. Check backend logs." {
		t.Fatalf("unexpected en fallback: %q", got)
	}
}

func TestFormatExpiryReconcileResultLocalized(t *testing.T) {
	result := users.ExpiryReconcileResult{}
	if got := formatExpiryReconcileResult(LocaleRU, result); got != "Найдено пользователей: 0, затронуто нод: 0, успешных синхронизаций: 0, обработано пользователей: 0" {
		t.Fatalf("unexpected ru reconcile summary: %q", got)
	}
	if got := formatExpiryReconcileResult(LocaleEN, result); got != "Users found: 0, nodes affected: 0, successful syncs: 0, users processed: 0" {
		t.Fatalf("unexpected en reconcile summary: %q", got)
	}
}
