package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUUIDParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("", UUIDParams("id"))
	ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	g.GET("/x/:id", ok)
	g.GET("/os/:os_id", ok)

	cases := []struct {
		path   string
		status int
	}{
		{"/x/6f1c2a7e-0001-4a51-9a3e-000000000001", http.StatusNoContent},
		{"/x/nao-e-uuid", http.StatusBadRequest},
		{"/x/123", http.StatusBadRequest},
		{"/os/seed-os-0001", http.StatusNoContent},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: esperado %d, veio %d", tc.path, tc.status, w.Code)
		}
		if tc.status != http.StatusBadRequest {
			continue
		}
		var resp ErrorResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil || resp.Error.Code != CodeInvalidRequest {
			t.Fatalf("%s: corpo inesperado %s", tc.path, w.Body.String())
		}
	}
}
