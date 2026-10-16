package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeRequiresReadinessAndBypassesProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, status := range []int{200, 503, 302} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health/ready" {
				t.Errorf("unexpected probe path: %s", r.URL.Path)
			}
			w.Header().Set("Location", "/health/ready")
			w.WriteHeader(status)
		}))
		address := strings.TrimPrefix(s.URL, "http://")
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range []string{address, ":" + port, "0.0.0.0:" + port} {
			err := Check(context.Background(), address)
			if (err == nil) != (status == http.StatusOK) {
				t.Errorf("status=%d address=%s: %v", status, address, err)
			}
		}
		s.Close()
	}
}
