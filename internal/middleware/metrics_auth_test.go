package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/metrics", MetricsAuth("monitor-secret"), func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, tc := range []struct {
		auth string
		want int
	}{{"", http.StatusUnauthorized}, {"Bearer wrong", http.StatusUnauthorized}, {"Bearer monitor-secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("auth=%q status=%d want=%d", tc.auth, w.Code, tc.want)
		}
	}
}
