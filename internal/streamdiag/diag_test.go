package streamdiag

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGateAndRedaction(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	for _, tt := range []struct {
		token, expiry, path string
		active              bool
	}{{"", "1100", "/api/v1/public/shares/demo/content/test.mp4?synthetic-diagnostic=1", false}, {"demo", "999", "/api/v1/public/shares/demo/content/test.mp4?synthetic-diagnostic=1", false}, {"demo", "1100", "/api/v1/public/shares/other/content/test.mp4?synthetic-diagnostic=1", false}, {"demo", "1100", "/api/v1/public/shares/demo/content/test.mp4", false}, {"demo", "1100", "/api/v1/public/shares/demo/content/test.mp4?synthetic-diagnostic=1", true}} {
		seen := false
		handler := Middleware(tt.token, tt.expiry, now)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = Active(r.Context())
			if seen {
				Mark(r.Context(), "open")
				Write(r.Context(), w, errors.New("PRIVATE SECRET SESSION"))
			}
		}))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", tt.path, nil))
		if seen != tt.active {
			t.Fatal(tt)
		}
		if strings.Contains(rr.Body.String(), "PRIVATE") {
			t.Fatal("leak")
		}
	}
}
func TestClassifyAndPanic(t *testing.T) {
	if Classify(context.DeadlineExceeded) != "timeout" {
		t.Fatal("timeout")
	}
	rr := httptest.NewRecorder()
	Middleware("demo", "1100", func() time.Time { return time.Unix(1000, 0) })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("PRIVATE") })).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/public/shares/demo/content/test.mp4?synthetic-diagnostic=1", nil))
	if strings.Contains(rr.Body.String(), "PRIVATE") || !strings.Contains(rr.Body.String(), "internal") {
		t.Fatal(rr.Body.String())
	}
}
