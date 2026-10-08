package station

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

func TestSuspendedSessionDrawsNoEnergy(t *testing.T) {
	cfg := &Config{DC: false, PowerKw: 22, Phases: 3}
	s := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 40, TargetSoc: 100, MaxAcKw: 11}}
	s.Suspended = true
	if p := s.powerW(cfg); p != 0 {
		t.Fatalf("a suspended EV draws no power, got %f W", p)
	}
	if done := s.Advance(cfg, time.Minute); done || s.EnergyWh != 0 || s.Battery.SocPercent != 40 {
		t.Fatalf("a suspended EV must not move: done=%v energy=%f soc=%f", done, s.EnergyWh, s.Battery.SocPercent)
	}
}

func TestStopRefusesAReasonOutsideTheSpec(t *testing.T) {
	st := New(Config{ID: "SIM-TEST-REASON", Connectors: 1}, "ws://unused", slog.Default())
	if err := st.StopCharge(1, "Unplugged"); err == nil || !strings.Contains(err.Error(), "not an OCPP 1.6 Reason") {
		t.Fatalf("a reason outside the 1.6 enum must be refused, got %v", err)
	}
	for _, r := range []string{"PowerLoss", "EmergencyStop", "Reboot", "Local", "Remote", "EVDisconnected"} {
		if !ValidStopReason(r) {
			t.Fatalf("%s is an OCPP 1.6 reason", r)
		}
	}
}

// TestEVSuspendKeepsTheTransactionOpen: the car stops drawing energy, the
// connector reports SuspendedEV, MeterValues keep coming with a flat
// register and 0 W, the cable stays locked — and the transaction ends only
// when the driver pulls the cable from the car.
func TestEVSuspendKeepsTheTransactionOpen(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-005", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 100, MaxAcKw: 7.4},
	}, wsURL, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, "boot", func() bool { return len(csms.seen()) > 0 })

	if err := st.SuspendEV(1); err == nil {
		t.Fatal("suspending without a transaction must fail")
	}
	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "tag", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "energy flowing", func() bool { return len(csms.payloads("MeterValues")) > 0 })
	if err := st.SuspendEV(1); err != nil {
		t.Fatal(err)
	}
	if err := st.SuspendEV(1); err == nil {
		t.Fatal("suspending twice must fail")
	}
	waitFor(t, "SuspendedEV status", func() bool {
		for _, p := range csms.payloads("StatusNotification") {
			var s ocpp.StatusNotificationReq
			if json.Unmarshal(p, &s) == nil && s.Status == string(SuspendedEV) {
				return true
			}
		}
		return false
	})
	if err := st.Unplug(1); err == nil {
		t.Fatal("the cable stays locked while the EV is suspended")
	}

	// Two more ticks: the register is flat and the power is zero.
	after := len(csms.payloads("MeterValues"))
	waitFor(t, "meter values while suspended", func() bool { return len(csms.payloads("MeterValues")) >= after+2 })
	all := csms.payloads("MeterValues")
	registers := map[string]bool{}
	for _, raw := range all[after:] {
		var mv ocpp.MeterValuesReq
		if err := json.Unmarshal(raw, &mv); err != nil {
			t.Fatal(err)
		}
		for _, v := range mv.MeterValue[0].SampledValue {
			switch v.Measurand {
			case "Energy.Active.Import.Register":
				registers[v.Value] = true
			case "Power.Active.Import":
				if v.Value != "0" {
					t.Fatalf("a suspended EV reports 0 W, got %s", v.Value)
				}
			}
		}
	}
	if len(registers) != 1 {
		t.Fatalf("the register must stay flat while suspended, got %v", registers)
	}
	for _, a := range csms.seen() {
		if a == "StopTransaction" {
			t.Fatal("suspending must not end the transaction")
		}
	}

	if err := st.DisconnectEV(1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop after the driver pulls the cable", func() bool { return len(csms.payloads("StopTransaction")) == 1 })
	var stop ocpp.StopTransactionReq
	if err := json.Unmarshal(csms.payloads("StopTransaction")[0], &stop); err != nil {
		t.Fatal(err)
	}
	if stop.Reason != "EVDisconnected" {
		t.Fatalf("stop reason: %q", stop.Reason)
	}
}

// TestStopCarriesTheGivenReason: the CSMS reads the reason it was told.
func TestStopCarriesTheGivenReason(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := New(Config{
		ID: "SIM-TEST-006", Vendor: "Test", Model: "TestBox", Connectors: 1,
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
	waitFor(t, "start", func() bool { return len(csms.payloads("StartTransaction")) == 1 })
	if err := st.StopCharge(1, "PowerLoss"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", func() bool { return len(csms.payloads("StopTransaction")) == 1 })
	var stop ocpp.StopTransactionReq
	if err := json.Unmarshal(csms.payloads("StopTransaction")[0], &stop); err != nil {
		t.Fatal(err)
	}
	if stop.Reason != "PowerLoss" {
		t.Fatalf("stop reason: %q", stop.Reason)
	}
}
