package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/newrelic/go-agent/v3/integrations/nrgin"
	"github.com/newrelic/go-agent/v3/newrelic"
)

func init() { gin.SetMode(gin.TestMode) }

const secret = "segredo-de-teste"

func token(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{"sub": "u1", "role": "client", "document": "52998224725", "exp": time.Now().Add(time.Hour).Unix()}
}

func authRouter() *gin.Engine {
	r := gin.New()
	r.Use(Auth(secret))
	r.GET("/me", func(c *gin.Context) {
		a := httputil.ActorFrom(c)
		c.JSON(200, gin.H{"id": a.UserID, "role": a.Role, "document": a.Document})
	})
	r.GET("/admin", RequireRole(entities.RoleAdmin), func(c *gin.Context) { c.Status(204) })
	return r
}

func get(r http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuth_TokenValido(t *testing.T) {
	w := get(authRouter(), "/me", map[string]string{"Authorization": "Bearer " + token(t, jwt.SigningMethodHS256, []byte(secret), validClaims())})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"document":"52998224725"`) || !strings.Contains(w.Body.String(), `"role":"client"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAuth_Recusas(t *testing.T) {
	expired := validClaims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	noExp := validClaims()
	delete(noExp, "exp")
	noSub := validClaims()
	delete(noSub, "sub")
	badRole := validClaims()
	badRole["role"] = "superuser"
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims()).SignedString(jwt.UnsafeAllowNoneSignatureType)

	cases := map[string]string{
		"sem header":         "",
		"esquema errado":     "Basic abc",
		"bearer vazio":       "Bearer ",
		"segredo errado":     "Bearer " + token(t, jwt.SigningMethodHS256, []byte("outro"), validClaims()),
		"expirado":           "Bearer " + token(t, jwt.SigningMethodHS256, []byte(secret), expired),
		"sem exp":            "Bearer " + token(t, jwt.SigningMethodHS256, []byte(secret), noExp),
		"sem sub":            "Bearer " + token(t, jwt.SigningMethodHS256, []byte(secret), noSub),
		"papel desconhecido": "Bearer " + token(t, jwt.SigningMethodHS256, []byte(secret), badRole),
		"alg none":           "Bearer " + none,
		"HS512":              "Bearer " + token(t, jwt.SigningMethodHS512, []byte(secret), validClaims()),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			w := get(authRouter(), "/me", map[string]string{"Authorization": header})
			if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "UNAUTHORIZED") || w.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	r := authRouter()
	client := token(t, jwt.SigningMethodHS256, []byte(secret), validClaims())
	if w := get(r, "/admin", map[string]string{"Authorization": "Bearer " + client}); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "FORBIDDEN") {
		t.Fatalf("cliente em rota de admin: %d", w.Code)
	}
	admin := token(t, jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": "a", "role": "admin", "exp": time.Now().Add(time.Hour).Unix()})
	if w := get(r, "/admin", map[string]string{"Authorization": "bearer " + admin}); w.Code != http.StatusNoContent {
		t.Fatalf("admin: %d", w.Code)
	}
}

func TestCorrelationID(t *testing.T) {
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/", func(c *gin.Context) { c.String(200, httputil.CorrelationID(c)) })

	if w := get(r, "/", map[string]string{CorrelationHeader: "abc-123"}); w.Body.String() != "abc-123" || w.Header().Get(CorrelationHeader) != "abc-123" {
		t.Fatalf("reaproveita o header: %q", w.Body.String())
	}
	if w := get(r, "/", map[string]string{"X-Request-ID": "req-9"}); w.Body.String() != "req-9" {
		t.Fatalf("aceita X-Request-ID: %q", w.Body.String())
	}
	for _, bad := range []string{"", "com espaço", "quebra\nde linha", strings.Repeat("a", 129)} {
		w := get(r, "/", map[string]string{CorrelationHeader: bad})
		if id := w.Body.String(); len(id) != 32 || id == bad || w.Header().Get(CorrelationHeader) != id {
			t.Fatalf("valor %q deveria ser trocado por um id gerado, veio %q", bad, id)
		}
	}
}

func TestRequestLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	r := gin.New()
	r.Use(CorrelationID(), RequestLogger(base, "/health/live"))
	r.GET("/health/live", func(c *gin.Context) { c.Status(200) })
	r.GET("/ok/:id", func(c *gin.Context) {
		logger.FromContext(c.Request.Context()).Info("dentro do handler")
		c.Set(httputil.KeyUserID, "u1")
		c.Status(200)
	})
	r.GET("/bad", func(c *gin.Context) { _ = c.Error(errors.New("campo inválido")); c.Status(400) })
	r.GET("/boom", func(c *gin.Context) { c.Status(500) })

	get(r, "/health/live", nil)
	if buf.Len() != 0 {
		t.Fatalf("health não gera log de acesso: %s", buf.String())
	}

	get(r, "/ok/1", map[string]string{CorrelationHeader: "corr-1"})
	get(r, "/bad", nil)
	get(r, "/boom", nil)

	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("linha inválida %q", l)
		}
		lines = append(lines, m)
	}
	if len(lines) != 4 {
		t.Fatalf("esperava 4 linhas, veio %d: %s", len(lines), buf.String())
	}
	if lines[0]["msg"] != "dentro do handler" || lines[0]["correlation_id"] != "corr-1" {
		t.Errorf("logger do contexto: %v", lines[0])
	}
	if lines[1]["level"] != "INFO" || lines[1]["route"] != "/ok/:id" || lines[1]["user_id"] != "u1" || lines[1]["status"] != float64(200) {
		t.Errorf("acesso: %v", lines[1])
	}
	if lines[2]["level"] != "WARN" || lines[2]["errors"] == nil {
		t.Errorf("4xx: %v", lines[2])
	}
	if lines[3]["level"] != "ERROR" {
		t.Errorf("5xx: %v", lines[3])
	}
}

func TestCORS(t *testing.T) {
	r := gin.New()
	r.Use(CORS([]string{"https://app.example.com/", "*", " "}))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })
	r.OPTIONS("/x", func(c *gin.Context) { c.Status(299) }) // não deve ser alcançado no preflight

	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/x", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := preflight("https://app.example.com"); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("preflight permitido: %d %v", w.Code, w.Header())
	}
	if w := preflight("https://evil.example.com"); w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" || !strings.Contains(w.Body.String(), "ORIGIN_NOT_ALLOWED") {
		t.Fatalf("preflight recusado: %d %v", w.Code, w.Header())
	}
	if w := get(r, "/x", map[string]string{"Origin": "https://evil.example.com"}); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("o curinga da lista é ignorado e a origem estranha não recebe CORS: %v", w.Header())
	}
	if w := get(r, "/x", map[string]string{"Origin": "https://app.example.com"}); w.Header().Get("Access-Control-Expose-Headers") != CorrelationHeader {
		t.Fatalf("expõe o header de correlação: %v", w.Header())
	}
	if w := get(r, "/x", nil); w.Code != 200 {
		t.Fatalf("sem Origin segue: %d", w.Code)
	}
}

func TestNewRelicContext(t *testing.T) {
	r := gin.New()
	r.Use(NewRelicContext())
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	if w := get(r, "/", nil); w.Code != 200 {
		t.Fatalf("sem agente o middleware é transparente: %d", w.Code)
	}

	app, err := newrelic.NewApplication(newrelic.ConfigAppName("billing-test"), newrelic.ConfigEnabled(false))
	if err != nil {
		t.Fatal(err)
	}
	r = gin.New()
	r.Use(nrgin.Middleware(app), NewRelicContext())
	r.GET("/", func(c *gin.Context) {
		if newrelic.FromContext(c.Request.Context()) == nil {
			c.Status(500)
			return
		}
		c.Status(200)
	})
	if w := get(r, "/", nil); w.Code != 200 {
		t.Fatal("a transação do nrgin precisa estar no ctx da requisição")
	}
}
