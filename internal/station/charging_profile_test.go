package station

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// profile builds a one-period-or-more profile; periods are (startPeriod, limit).
func profile(id, stack int, purpose, kind, unit string, periods ...[2]float64) ocpp.ChargingProfile {
	p := ocpp.ChargingProfile{
		ChargingProfileID: id, StackLevel: stack, ChargingProfilePurpose: purpose,
		ChargingProfileKind: kind,
		ChargingSchedule:    ocpp.ChargingSchedule{ChargingRateUnit: unit},
	}
	for _, pr := range periods {
		p.ChargingSchedule.ChargingSchedulePeriod = append(p.ChargingSchedule.ChargingSchedulePeriod,
			ocpp.ChargingSchedulePeriod{StartPeriod: int(pr[0]), Limit: pr[1]})
	}
	return p
}

// chargingStation is an offline three-phase 22 kW AC station with a car that
// can take 22 kW on every connector, all charging — so the profile is the
// only thing limiting the power.
func chargingStation(t *testing.T, connectors int, dc bool) *Station {
	t.Helper()
	st := quietStation(t, Config{
		ID: "SIM-SC", Connectors: connectors, PowerKw: 22, Phases: 3, DC: dc,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 100, MaxAcKw: 22, MaxDcKw: 22},
	}, "ws://unused")
	for i := 1; i <= connectors; i++ {
		if err := st.Plug(i); err != nil {
			t.Fatal(err)
		}
		if err := st.StartCharge(i, "tag", nil); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func (s *Station) set(t *testing.T, connector int, p ocpp.ChargingProfile) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	status := s.setChargingProfileLocked(ocpp.SetChargingProfileReq{ConnectorID: connector, CsChargingProfiles: p}, now)
	s.applyProfilesLocked(now)
	return status
}

func (s *Station) clear(t *testing.T, req ocpp.ClearChargingProfileReq) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.clearChargingProfileLocked(req)
	s.applyProfilesLocked(time.Now())
	return status
}

// power returns the connector's power right now and its state.
func (s *Station) power(connector int) (float64, State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectors[connector-1]
	return c.Session.powerW(&s.Config), c.State
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.5 }

func TestLimitToW(t *testing.T) {
	for _, tc := range []struct {
		limit  float64
		unit   string
		phases int
		want   float64
	}{
		{16, "A", 3, 16 * 230 * 3}, // 11 040 W
		{32, "A", 1, 32 * 230},     // 7 360 W
		{6, "A", 3, 4140},
		{7400, "W", 3, 7400},
	} {
		if got := limitToW(tc.limit, tc.unit, tc.phases); !near(got, tc.want) {
			t.Errorf("%v %s on %d phases = %v W, want %v", tc.limit, tc.unit, tc.phases, got, tc.want)
		}
	}
}

// The car follows the limit, stops under 6 A (SuspendedEVSE, 0 W) and comes
// back by itself when the limit allows 6 A again.
func TestLimitStopsUnderSixAmpsAndResumes(t *testing.T) {
	st := chargingStation(t, 1, false)
	if p, s := st.power(1); !near(p, 22000) || s != Charging {
		t.Fatalf("no profile: %v W %s", p, s)
	}
	steps := []struct {
		amps  float64
		watts float64
		state State
	}{
		{16, 11040, Charging},
		{5.9, 0, SuspendedEVSE},
		{0, 0, SuspendedEVSE},
		{6, 4140, Charging},
		{10, 6900, Charging},
	}
	for _, step := range steps {
		if got := st.set(t, 0, profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, step.amps})); got != "Accepted" {
			t.Fatalf("%v A: %s", step.amps, got)
		}
		if p, s := st.power(1); !near(p, step.watts) || s != step.state {
			t.Fatalf("%v A: got %v W %s, want %v W %s", step.amps, p, s, step.watts, step.state)
		}
	}
	// A limit in W is held to the same floor: 4000 W on three phases is 5.8 A.
	st.set(t, 0, profile(1, 0, purposeTxDefault, "Absolute", "W", [2]float64{0, 4000}))
	if p, s := st.power(1); p != 0 || s != SuspendedEVSE {
		t.Fatalf("4000 W on 3 phases is under 6 A: %v W %s", p, s)
	}
	// Clearing the profile gives the car its full power back.
	if got := st.clear(t, ocpp.ClearChargingProfileReq{}); got != "Accepted" {
		t.Fatal(got)
	}
	if p, s := st.power(1); !near(p, 22000) || s != Charging {
		t.Fatalf("after clear: %v W %s", p, s)
	}
}

// numberPhases on the period counts: 16 A on one phase is 3 680 W, not 11 040.
func TestNumberPhasesOfThePeriod(t *testing.T) {
	st := chargingStation(t, 1, false)
	p := profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 16})
	one := 1
	p.ChargingSchedule.ChargingSchedulePeriod[0].NumberPhases = &one
	st.set(t, 1, p)
	if w, s := st.power(1); !near(w, 3680) || s != Charging {
		t.Fatalf("16 A on one phase: %v W %s", w, s)
	}
}

// TxProfile beats TxDefaultProfile whatever the stack levels; it needs a
// running transaction, and it dies with it.
func TestTxProfileOverridesTxDefault(t *testing.T) {
	st := chargingStation(t, 1, false)
	st.set(t, 0, profile(1, 5, purposeTxDefault, "Absolute", "A", [2]float64{0, 8}))
	st.set(t, 1, profile(2, 0, purposeTx, "Absolute", "A", [2]float64{0, 20}))
	if p, _ := st.power(1); !near(p, 20*690) {
		t.Fatalf("TxProfile 20 A should win over TxDefault 8 A: %v W", p)
	}
	// The connector's own TxDefault wins over the one on connector 0.
	st.clear(t, ocpp.ClearChargingProfileReq{ChargingProfilePurpose: purposeTx})
	st.set(t, 1, profile(3, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 12}))
	if p, _ := st.power(1); !near(p, 12*690) {
		t.Fatalf("connector TxDefault 12 A should win over connector-0 8 A: %v W", p)
	}

	st.set(t, 1, profile(4, 0, purposeTx, "Absolute", "A", [2]float64{0, 7}))
	if err := st.StopCharge(1, "Local"); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	for _, ip := range st.profiles {
		if ip.profile.ChargingProfilePurpose == purposeTx {
			t.Errorf("TxProfile %d survived its transaction", ip.profile.ChargingProfileID)
		}
	}
	st.mu.Unlock()
	if got := st.set(t, 1, profile(5, 0, purposeTx, "Absolute", "A", [2]float64{0, 7})); got != "Rejected" {
		t.Fatalf("a TxProfile with no transaction must be Rejected, got %s", got)
	}
}

// Higher stackLevel wins within a purpose, and a profile past its duration
// gives way to the one below it.
func TestStackLevelAndSchedulePeriods(t *testing.T) {
	st := chargingStation(t, 1, false)
	st.set(t, 0, profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 10}))
	// Started 90 s ago: the 16 A first minute is over, 8 A is the active period.
	p := profile(2, 1, purposeTxDefault, "Absolute", "A", [2]float64{0, 16}, [2]float64{60, 8})
	p.ChargingSchedule.StartSchedule = time.Now().Add(-90 * time.Second).UTC().Format(time.RFC3339)
	st.set(t, 0, p)
	if w, _ := st.power(1); !near(w, 8*690) {
		t.Fatalf("stack 1, second period (8 A) expected: %v W", w)
	}
	// Same schedule with a 60 s duration: expired, stack 0 (10 A) applies.
	d := 60
	p.ChargingSchedule.Duration = &d
	st.set(t, 0, p)
	if w, _ := st.power(1); !near(w, 10*690) {
		t.Fatalf("expired stack 1 should fall back to stack 0 (10 A): %v W", w)
	}
}

// Relative schedules run from the start of the transaction.
func TestRelativeScheduleFollowsTheTransaction(t *testing.T) {
	st := chargingStation(t, 1, false)
	st.mu.Lock()
	st.connectors[0].Session.StartedAt = time.Now().Add(-2 * time.Minute)
	st.mu.Unlock()
	st.set(t, 1, profile(1, 0, purposeTx, "Relative", "W", [2]float64{0, 11000}, [2]float64{60, 7000}))
	if w, _ := st.power(1); !near(w, 7000) {
		t.Fatalf("two minutes in, the second period applies: %v W", w)
	}
}

// Recurring Daily: the offset into the current day decides the period.
func TestRecurringDaily(t *testing.T) {
	st := chargingStation(t, 1, false)
	p := profile(1, 0, purposeTxDefault, "Recurring", "A", [2]float64{0, 16}, [2]float64{3600, 8})
	p.RecurrencyKind = "Daily"
	// Three days and 2 h ago: two hours into today's cycle → 8 A.
	p.ChargingSchedule.StartSchedule = time.Now().Add(-74 * time.Hour).UTC().Format(time.RFC3339)
	st.set(t, 0, p)
	if w, _ := st.power(1); !near(w, 8*690) {
		t.Fatalf("Daily recurring, 2 h into the cycle: %v W", w)
	}
}

// ChargePointMaxProfile caps the whole station and is shared; when it cannot
// give every car 6 A, one waits at zero and the other charges.
func TestChargePointMaxIsSharedWithTheFloor(t *testing.T) {
	st := chargingStation(t, 2, false)
	st.set(t, 0, profile(1, 0, purposeChargePointMax, "Absolute", "A", [2]float64{0, 20}))
	for i := 1; i <= 2; i++ {
		if w, s := st.power(i); !near(w, 10*690) || s != Charging {
			t.Fatalf("connector %d: 20 A shared by two = 10 A each, got %v W %s", i, w, s)
		}
	}
	// 10 A for two cars is 5 A each — under the floor: connector 2 waits.
	st.set(t, 0, profile(1, 0, purposeChargePointMax, "Absolute", "A", [2]float64{0, 10}))
	if w, s := st.power(1); !near(w, 10*690) || s != Charging {
		t.Fatalf("connector 1 should take the whole 10 A: %v W %s", w, s)
	}
	if w, s := st.power(2); w != 0 || s != SuspendedEVSE {
		t.Fatalf("connector 2 should wait at zero: %v W %s", w, s)
	}
	// A TxProfile under the station cap still wins for its connector, and
	// the cap left over goes to the other one.
	st.set(t, 0, profile(1, 0, purposeChargePointMax, "Absolute", "A", [2]float64{0, 24}))
	st.set(t, 1, profile(2, 0, purposeTx, "Absolute", "A", [2]float64{0, 6}))
	if w, _ := st.power(1); !near(w, 6*690) {
		t.Fatalf("connector 1 held by its TxProfile at 6 A: %v W", w)
	}
	if w, s := st.power(2); !near(w, 18*690) || s != Charging {
		t.Fatalf("connector 2 gets the remaining 18 A: %v W %s", w, s)
	}
}

func TestSetChargingProfileValidation(t *testing.T) {
	st := chargingStation(t, 1, false)
	bad := map[string]struct {
		connector int
		p         ocpp.ChargingProfile
	}{
		"station cap on a connector": {1, profile(1, 0, purposeChargePointMax, "Absolute", "A", [2]float64{0, 16})},
		"TxProfile on connector 0":   {0, profile(1, 0, purposeTx, "Absolute", "A", [2]float64{0, 16})},
		"unknown unit":               {0, profile(1, 0, purposeTxDefault, "Absolute", "kW", [2]float64{0, 16})},
		"no periods":                 {0, profile(1, 0, purposeTxDefault, "Absolute", "A")},
		"first period not at 0":      {0, profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{10, 16})},
		"connector out of range":     {2, profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 16})},
		"recurring without kind":     {0, profile(1, 0, purposeTxDefault, "Recurring", "A", [2]float64{0, 16})},
		"stack level above the max":  {0, profile(1, maxStackLevel+1, purposeTxDefault, "Absolute", "A", [2]float64{0, 16})},
	}
	for name, tc := range bad {
		if got := st.set(t, tc.connector, tc.p); got != "Rejected" {
			t.Errorf("%s: got %s, want Rejected", name, got)
		}
	}
	// A TxProfile for another transaction is Rejected too.
	st.mu.Lock()
	st.connectors[0].Session.TransactionID = 42
	st.mu.Unlock()
	p := profile(1, 0, purposeTx, "Absolute", "A", [2]float64{0, 16})
	other := 41
	p.TransactionID = &other
	if got := st.set(t, 1, p); got != "Rejected" {
		t.Errorf("TxProfile for transaction 41 on 42: %s", got)
	}
}

func TestClearChargingProfileFilters(t *testing.T) {
	st := chargingStation(t, 1, false)
	st.set(t, 0, profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 16}))
	st.set(t, 0, profile(2, 1, purposeTxDefault, "Absolute", "A", [2]float64{0, 10}))
	st.set(t, 0, profile(3, 0, purposeChargePointMax, "Absolute", "A", [2]float64{0, 32}))
	// Same stackLevel + purpose + connector replaces instead of piling up.
	st.set(t, 0, profile(4, 1, purposeTxDefault, "Absolute", "A", [2]float64{0, 12}))
	count := func() int { st.mu.Lock(); defer st.mu.Unlock(); return len(st.profiles) }
	if n := count(); n != 3 {
		t.Fatalf("profile 4 should replace profile 2: %d installed", n)
	}
	if got := st.clear(t, ocpp.ClearChargingProfileReq{ID: ptr(99)}); got != "Unknown" {
		t.Fatalf("unknown id: %s", got)
	}
	if got := st.clear(t, ocpp.ClearChargingProfileReq{StackLevel: ptr(1), ChargingProfilePurpose: purposeTxDefault}); got != "Accepted" || count() != 2 {
		t.Fatalf("by stackLevel+purpose: %s, %d left", got, count())
	}
	if w, _ := st.power(1); !near(w, 16*690) {
		t.Fatalf("stack 0 (16 A) should apply again: %v W", w)
	}
	if got := st.clear(t, ocpp.ClearChargingProfileReq{ConnectorID: ptr(1)}); got != "Unknown" {
		t.Fatalf("nothing on connector 1: %s", got)
	}
	if got := st.clear(t, ocpp.ClearChargingProfileReq{ID: ptr(3)}); got != "Accepted" || count() != 1 {
		t.Fatalf("by id: %s, %d left", got, count())
	}
}

func TestSupportedFeatureProfiles(t *testing.T) {
	on := quietStation(t, Config{ID: "SIM-SC-ON", Connectors: 1}, "")
	if got := configValue(t, on, "SupportedFeatureProfiles"); got != "Core,Reservation,RemoteTrigger,SmartCharging" {
		t.Fatalf("default station: %s", got)
	}
	if got := configValue(t, on, "ChargingScheduleAllowedChargingRateUnit"); got != "Current,Power" {
		t.Fatalf("allowed units: %s", got)
	}
	no := false
	off := quietStation(t, Config{ID: "SIM-SC-OFF", Connectors: 1, SmartCharging: &no}, "")
	if got := configValue(t, off, "SupportedFeatureProfiles"); got != "Core,Reservation,RemoteTrigger" {
		t.Fatalf("smartCharging: false: %s", got)
	}
	if conf := off.getConfiguration([]string{"ChargeProfileMaxStackLevel"}); len(conf.UnknownKey) != 1 {
		t.Fatalf("a station without SmartCharging has no SmartCharging keys: %+v", conf)
	}
}

// End to end: the CSMS sends the profile, MeterValues show the reduced power,
// the connector reports SuspendedEVSE under 6 A and Charging again above.
func TestChargingProfileEndToEnd(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	st := quietStation(t, Config{
		ID: "SIM-SC-E2E", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, Phases: 3, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 100, MaxAcKw: 22},
	}, wsURL)
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

	setAmps := func(amps float64) {
		t.Helper()
		raw := csms.call(t, "SetChargingProfile", ocpp.SetChargingProfileReq{
			ConnectorID:        1,
			CsChargingProfiles: profile(7, 0, purposeTx, "Relative", "A", [2]float64{0, amps}),
		})
		var conf ocpp.SetChargingProfileConf
		if err := json.Unmarshal(raw, &conf); err != nil || conf.Status != "Accepted" {
			t.Fatalf("SetChargingProfile %v A: %s", amps, raw)
		}
	}
	lastPower := func() string {
		mv := csms.payloads("MeterValues")
		if len(mv) == 0 {
			return ""
		}
		var req ocpp.MeterValuesReq
		_ = json.Unmarshal(mv[len(mv)-1], &req)
		for _, v := range req.MeterValue[0].SampledValue {
			if v.Measurand == measurandPower {
				return v.Value
			}
		}
		return ""
	}
	lastStatus := func() string {
		sn := csms.payloads("StatusNotification")
		var s ocpp.StatusNotificationReq
		_ = json.Unmarshal(sn[len(sn)-1], &s)
		return s.Status
	}

	setAmps(10)
	waitFor(t, "MeterValues at 10 A (6900 W)", func() bool { return lastPower() == "6900" })
	setAmps(4)
	waitFor(t, "SuspendedEVSE under 6 A", func() bool { return lastStatus() == string(SuspendedEVSE) })
	waitFor(t, "MeterValues at 0 W", func() bool { return lastPower() == "0" })
	setAmps(6)
	waitFor(t, "Charging again at 6 A", func() bool { return lastStatus() == string(Charging) })
	waitFor(t, "MeterValues at 6 A (4140 W)", func() bool { return lastPower() == "4140" })
	raw := csms.call(t, "ClearChargingProfile", ocpp.ClearChargingProfileReq{ID: ptr(7)})
	if !strings.Contains(string(raw), "Accepted") {
		t.Fatalf("ClearChargingProfile: %s", raw)
	}
	waitFor(t, "full power after clear", func() bool { return lastPower() == "22000" })
}

// A station without SmartCharging answers like firmware without the
// profile: a NotImplemented CALLERROR, not a CALLRESULT.
func TestNoSmartChargingAnswersNotImplemented(t *testing.T) {
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	no := false
	st := quietStation(t, Config{ID: "SIM-SC-NONE", Vendor: "Test", Model: "TestBox", Connectors: 1, SmartCharging: &no}, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, "boot", func() bool { return len(csms.seen()) > 0 })

	for _, action := range []string{"SetChargingProfile", "ClearChargingProfile"} {
		var payload any = ocpp.ClearChargingProfileReq{}
		if action == "SetChargingProfile" {
			payload = ocpp.SetChargingProfileReq{CsChargingProfiles: profile(1, 0, purposeTxDefault, "Absolute", "A", [2]float64{0, 16})}
		}
		if got := string(csms.call(t, action, payload)); got != `{"callError":"NotImplemented"}` {
			t.Fatalf("%s: got %s, want a NotImplemented CALLERROR", action, got)
		}
	}
}
