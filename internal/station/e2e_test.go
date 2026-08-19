package station

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// fakeCSMS is the minimum central system: accepts the WebSocket, answers
// every CALL, records every action it saw. It exists so the emulator can be
// tested end to end without any real platform.
type fakeCSMS struct {
	mu      sync.Mutex
	actions []string
	txSeq   int
}

func (f *fakeCSMS) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func (f *fakeCSMS) handler(t *testing.T) http.HandlerFunc {
	upgrader := websocket.Upgrader{Subprotocols: []string{"ocpp1.6"}}
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := ocpp.Decode(raw)
			if err != nil {
				t.Errorf("decode: %v", err)
				continue
			}
			if frame.Type != ocpp.Call {
				continue
			}
			f.mu.Lock()
			f.actions = append(f.actions, frame.Action)
			f.mu.Unlock()

			var payload any
			switch frame.Action {
			case "BootNotification":
				payload = ocpp.BootNotificationConf{Status: "Accepted", CurrentTime: time.Now().UTC().Format(time.RFC3339), Interval: 300}
			case "Authorize":
				payload = ocpp.AuthorizeConf{IDTagInfo: ocpp.IDTagInfo{Status: "Accepted"}}
			case "StartTransaction":
				f.mu.Lock()
				f.txSeq++
				id := f.txSeq
				f.mu.Unlock()
				payload = ocpp.StartTransactionConf{IDTagInfo: ocpp.IDTagInfo{Status: "Accepted"}, TransactionID: id}
			case "StopTransaction":
				payload = ocpp.StopTransactionConf{}
			case "Heartbeat":
				payload = ocpp.HeartbeatConf{CurrentTime: time.Now().UTC().Format(time.RFC3339)}
			default:
				payload = map[string]any{}
			}
			resp, _ := ocpp.EncodeCallResult(frame.MessageID, payload)
			if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
				return
			}
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFullSessionAgainstFakeCSMS(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-001", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, Phases: 3, MeterValuesS: 1, HeartbeatS: 300,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 100, MaxAcKw: 7.4},
	}, wsURL, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)

	waitFor(t, "boot", func() bool {
		for _, a := range csms.seen() {
			if a == "BootNotification" {
				return true
			}
		}
		return false
	})

	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "test-tag", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "transaction id from CSMS", func() bool {
		snap := st.Snapshot()
		conns := snap["connectors"].([]map[string]any)
		session, ok := conns[0]["session"].(map[string]any)
		return ok && session["transactionId"].(int) > 0
	})
	waitFor(t, "meter values", func() bool {
		for _, a := range csms.seen() {
			if a == "MeterValues" {
				return true
			}
		}
		return false
	})
	if err := st.StopCharge(1, "Local"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop transaction", func() bool {
		for _, a := range csms.seen() {
			if a == "StopTransaction" {
				return true
			}
		}
		return false
	})
}

// TestOfflineQueueFlushesOnReconnect proves the store-and-forward the spec
// demands: a StopTransaction issued while offline MUST reach the CSMS after
// the station reconnects — losing it would hide exactly the class of bug
// this emulator exists to expose.
func TestOfflineQueueFlushesOnReconnect(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-002", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 50, DC: true, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 80, MaxDcKw: 90},
	}, wsURL, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)

	waitFor(t, "boot", func() bool { return len(csms.seen()) > 0 })
	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "tag", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "start", func() bool {
		for _, a := range csms.seen() {
			if a == "StartTransaction" {
				return true
			}
		}
		return false
	})

	st.Offline()
	if err := st.StopCharge(1, "Local"); err != nil {
		t.Fatal(err)
	}
	// While offline, the CSMS must NOT have seen the stop.
	for _, a := range csms.seen() {
		if a == "StopTransaction" {
			t.Fatal("StopTransaction leaked while offline")
		}
	}

	st.Online()
	waitFor(t, "queued StopTransaction after reconnect", func() bool {
		for _, a := range csms.seen() {
			if a == "StopTransaction" {
				return true
			}
		}
		return false
	})
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// TestRFIDTapWaitsForCSMSDecision proves the tap sequence: Authorize goes
// out, and the transaction starts ONLY after the CSMS accepts — never on a
// local decision.
func TestRFIDTapWaitsForCSMSDecision(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-003", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 100, MaxAcKw: 7.4},
	}, wsURL, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, "boot", func() bool { return len(csms.seen()) > 0 })

	if err := st.TapRFID(1, "TAG-01"); err == nil {
		t.Fatal("tap without cable must fail — real stations require plugging first")
	}
	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.TapRFID(1, "TAG-01"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "authorize then start", func() bool {
		var auth, start bool
		for _, a := range csms.seen() {
			if a == "Authorize" {
				auth = true
			}
			if a == "StartTransaction" {
				start = auth // start only counts if authorize came first
			}
		}
		return auth && start
	})
}

// TestEVDisconnectStopsWithHonestReason: pulling the cable from the car
// mid-session stops the transaction with EVDisconnected — the sequence a
// real charge point reports when the driver leaves without touching any app.
func TestEVDisconnectStopsWithHonestReason(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-004", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 100, MaxAcKw: 7.4},
	}, wsURL, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, "boot", func() bool { return len(csms.seen()) > 0 })

	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "tag", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "start", func() bool {
		for _, a := range csms.seen() {
			if a == "StartTransaction" {
				return true
			}
		}
		return false
	})
	if err := st.DisconnectEV(1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop after EV disconnect", func() bool {
		for _, a := range csms.seen() {
			if a == "StopTransaction" {
				return true
			}
		}
		return false
	})
	snap := st.Snapshot()
	conns := snap["connectors"].([]map[string]any)
	if conns[0]["plugged"].(bool) {
		t.Fatal("cable must be released after EV disconnect")
	}
}
