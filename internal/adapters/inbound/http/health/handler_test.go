package healthhandler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func serve(h *Handler, path string) (int, Status) {
	r := gin.New()
	r.GET("/health/live", h.Live)
	r.GET("/health/ready", h.Ready)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var s Status
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	return w.Code, s
}

func TestLive(t *testing.T) {
	failing := NewHandler(map[string]Check{"database": func(context.Context) error { return errors.New("fora") }}, 0)
	if code, s := serve(failing, "/health/live"); code != 200 || s.Status != "ok" {
		t.Fatalf("liveness não depende das dependências: %d %+v", code, s)
	}
}

func TestReady(t *testing.T) {
	ok := func(context.Context) error { return nil }
	h := NewHandler(map[string]Check{"database": ok, "aws": ok}, time.Second)
	if code, s := serve(h, "/health/ready"); code != 200 || s.Checks["database"] != "ok" || s.Checks["aws"] != "ok" {
		t.Fatalf("%d %+v", code, s)
	}

	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	h = NewHandler(map[string]Check{"database": ok, "aws": slow}, 20*time.Millisecond)
	code, s := serve(h, "/health/ready")
	if code != 503 || s.Status != "unavailable" || s.Checks["database"] != "ok" || s.Checks["aws"] == "ok" {
		t.Fatalf("dependência lenta deve estourar o timeout e tirar o pod do Service: %d %+v", code, s)
	}
}
