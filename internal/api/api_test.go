package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/luum-ev/ocpp-lab/internal/fleet"
	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// startFakeCSMS is the same minimal central system the station e2e tests
// use: accept, answer, remember. The API tests run against a REAL fleet
// connected to it — the control plane is tested end to end, not mocked.
func startFakeCSMS(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var actions []string
	up := websocket.Upgrader{Subprotocols: []string{"ocpp1.6"}}
	txSeq := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f, err := ocpp.Decode(raw)
			if err != nil || f.Type != ocpp.Call {
				continue
			}
			<-mu
			actions = append(actions, f.Action)
			mu <- struct{}{}
			var payload any
			switch f.Action {
			case "BootNotification":
				payload = ocpp.BootNotificationConf{Status: "Accepted", CurrentTime: time.Now().UTC().Format(time.RFC3339), Interval: 300}
			case "StartTransaction":
				txSeq++
				payload = ocpp.StartTransactionConf{IDTagInfo: ocpp.IDTagInfo{Status: "Accepted"}, TransactionID: txSeq}
			default:
				payload = map[string]any{}
			}
			resp, _ := ocpp.EncodeCallResult(f.MessageID, payload)
			_ = conn.WriteMessage(websocket.TextMessage, resp)
		}
	}))
	seen := func() []string {
		<-mu
		defer func() { mu <- struct{}{} }()
		return append([]string(nil), actions...)
	}
	return server, seen
}

func startAPI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	csms, seen := startFakeCSMS(t)
	t.Cleanup(csms.Close)
	wsURL := "ws" + strings.TrimPrefix(csms.URL, "http")

	fleetFile := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(fleetFile, []byte(`
csms: `+wsURL+`
stations:
  - id: API-TEST-01
    vendor: Test
    model: TestBox
    connectors: 2
    powerKw: 22
    meterValuesS: 1
    battery: { capacityKwh: 60, socPercent: 50, targetSoc: 100, maxAcKw: 7.4 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fl, err := fleet.Load(fleetFile, "", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go fl.Run(ctx)

	api := httptest.NewServer((&Server{Fleet: fl, Log: slog.Default()}).Handler())
	t.Cleanup(api.Close)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range seen() {
			if a == "BootNotification" {
				return api, seen
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("station never booted against the fake CSMS")
	return nil, nil
}

func do(t *testing.T, method, url string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestHealthz(t *testing.T) {
	api, _ := startAPI(t)
	code, body := do(t, "GET", api.URL+"/healthz")
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("healthz: %d %v", code, body)
	}
}

func TestFullFlowThroughAPI(t *testing.T) {
	api, seen := startAPI(t)

	if code, body := do(t, "POST", api.URL+"/stations/API-TEST-01/connectors/1/plug"); code != 200 {
		t.Fatalf("plug: %d %v", code, body)
	}
	// Plugging twice must conflict — the API surfaces state machine errors.
	if code, _ := do(t, "POST", api.URL+"/stations/API-TEST-01/connectors/1/plug"); code != http.StatusConflict {
		t.Fatalf("double plug should be 409, got %d", code)
	}
	if code, body := do(t, "POST", api.URL+"/stations/API-TEST-01/connectors/1/charge"); code != 200 {
		t.Fatalf("charge: %d %v", code, body)
	}
	if code, body := do(t, "POST", api.URL+"/stations/API-TEST-01/connectors/1/stop"); code != 200 {
		t.Fatalf("stop: %d %v", code, body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := map[string]bool{}
		for _, a := range seen() {
			got[a] = true
		}
		if got["StartTransaction"] && got["StopTransaction"] && got["StatusNotification"] {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("CSMS did not see the full flow: %v", seen())
}

func TestUnknownStationIs404(t *testing.T) {
	api, _ := startAPI(t)
	if code, _ := do(t, "POST", api.URL+"/stations/NOPE/kill"); code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

func TestFaultShowsUpInStatus(t *testing.T) {
	api, _ := startAPI(t)
	if code, _ := do(t, "POST", api.URL+"/stations/API-TEST-01/connectors/2/fault"); code != 200 {
		t.Fatalf("fault failed: %d", code)
	}
	resp, err := http.Get(api.URL + "/stations")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var stations []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&stations); err != nil {
		t.Fatal(err)
	}
	conns := stations[0]["connectors"].([]any)
	second := conns[1].(map[string]any)
	if second["state"] != "Faulted" {
		t.Fatalf("connector 2 should be Faulted, got %v", second["state"])
	}
}
