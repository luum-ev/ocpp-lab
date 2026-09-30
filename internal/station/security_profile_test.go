package station

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Security Profile 2: the upgrade carries Basic Auth with the station id as
// user and the AuthorizationKey as password. A CSMS that demands it lets the
// keyed station in and refuses the one without a key before the upgrade.
func TestSecurityProfile2SendsBasicAuth(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef01234567"
	csms := &fakeCSMS{}
	var refused atomic.Int32
	inner := csms.handler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		user, pass, ok := r.BasicAuth()
		if !ok || user != id || pass != key {
			refused.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner(w, r)
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keyless := New(Config{ID: "SIM-NOKEY", Vendor: "Test", Model: "TestBox", Connectors: 1, PowerKw: 7}, wsURL, log)
	go keyless.Run(ctx)
	waitFor(t, "the keyless station is refused", func() bool { return refused.Load() > 0 })

	keyed := New(Config{
		ID: "SIM-KEYED", Vendor: "Test", Model: "TestBox", Connectors: 1, PowerKw: 7,
		AuthorizationKey: key,
	}, wsURL, log)
	go keyed.Run(ctx)
	waitFor(t, "the keyed station boots", func() bool {
		for _, a := range csms.seen() {
			if a == "BootNotification" {
				return true
			}
		}
		return false
	})
	if keyless.Snapshot()["online"] == true {
		t.Fatal("the station without a key must not be online")
	}
	if _, leaked := keyed.Snapshot()["authorizationKey"]; leaked {
		t.Fatal("the key must never be part of the API snapshot")
	}
}
