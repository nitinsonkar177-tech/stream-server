// Package streamdiag is a temporary, default-off synthetic request diagnostic.
package streamdiag

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type key struct{}
type Trace struct {
	mu      sync.Mutex
	Stages  []string `json:"stages"`
	Failure string   `json:"failure,omitempty"`
}

func Active(ctx context.Context) bool { _, ok := ctx.Value(key{}).(*Trace); return ok }
func Mark(ctx context.Context, stage string) {
	if t, ok := ctx.Value(key{}).(*Trace); ok {
		t.mu.Lock()
		t.Stages = append(t.Stages, stage)
		t.mu.Unlock()
	}
}
func Classify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "upstream"
}
func Write(ctx context.Context, w http.ResponseWriter, err error) {
	t, ok := ctx.Value(key{}).(*Trace)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Failure = Classify(err)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(t)
}

// Middleware never exposes a general status endpoint. A token, near-term expiry,
// exact public-content path and explicit query opt-in must all match.
func Middleware(token, expiry string, now func() time.Time) func(http.Handler) http.Handler {
	until, _ := strconv.ParseInt(expiry, 10, 64)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			match := len(parts) >= 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "public" && parts[3] == "shares" && parts[5] == "content"
			delta := until - now().Unix()
			if token == "" || delta <= 0 || delta > 3600 || r.Method != "GET" || r.URL.Query().Get("synthetic-diagnostic") != "1" || !match || subtle.ConstantTimeCompare([]byte(parts[4]), []byte(token)) != 1 {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, key{}, &Trace{Stages: []string{"request"}})
			defer func() {
				if recover() != nil {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Cache-Control", "no-store")
					_, _ = w.Write([]byte(`{"stages":["request"],"failure":"internal"}`))
				}
			}()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Protect catches only a diagnostic operation's panic without retaining its value.
func Protect(ctx context.Context, fn func() error) (err error) {
	if !Active(ctx) {
		return fn()
	}
	defer func() {
		if recover() != nil {
			err = errors.New("synthetic internal failure")
			Mark(ctx, "internal-failure")
		}
	}()
	return fn()
}
