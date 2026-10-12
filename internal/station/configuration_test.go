package station

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

func quietStation(t *testing.T, cfg Config, csms string) *Station {
	t.Helper()
	return New(cfg, csms, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))
}

func configValue(t *testing.T, st *Station, key string) string {
	t.Helper()
	conf := st.getConfiguration([]string{key})
	if len(conf.ConfigurationKey) != 1 || conf.ConfigurationKey[0].Value == nil {
		t.Fatalf("GetConfiguration %s: %+v", key, conf)
	}
	return *conf.ConfigurationKey[0].Value
}

func TestMeterValueSampleIntervalIsValidated(t *testing.T) {
	st := quietStation(t, Config{ID: "SIM-CFG-01", MeterValuesS: 30}, "")
	for _, tc := range []struct {
		value, status, stored string
	}{
		{"15", "Accepted", "15"},
		{"0", "Accepted", "0"}, // 0 disables periodic sampling
		{"86400", "Accepted", "86400"},
		{"-1", "Rejected", "86400"},
		{"abc", "Rejected", "86400"},
		{"1.5", "Rejected", "86400"},
		{"", "Rejected", "86400"},
		{"86401", "Rejected", "86400"},
	} {
		if got := st.changeConfiguration("MeterValueSampleInterval", tc.value); got != tc.status {
			t.Fatalf("MeterValueSampleInterval=%q: status %s, want %s", tc.value, got, tc.status)
		}
		if got := configValue(t, st, "MeterValueSampleInterval"); got != tc.stored {
			t.Fatalf("after %q the station reports %s, want %s", tc.value, got, tc.stored)
		}
	}
}

func TestAnAcceptedIntervalWakesTheMeterLoop(t *testing.T) {
	st := quietStation(t, Config{ID: "SIM-CFG-02"}, "")
	if st.changeConfiguration("MeterValueSampleInterval", "nope") != "Rejected" {
		t.Fatal("garbage must be rejected")
	}
	select {
	case <-st.meterConfig:
		t.Fatal("a rejected value must not touch the meter loop")
	default:
	}
	for _, key := range []string{"MeterValueSampleInterval", "ClockAlignedDataInterval"} {
		if st.changeConfiguration(key, "10") != "Accepted" {
			t.Fatalf("%s=10 must be accepted", key)
		}
		select {
		case <-st.meterConfig:
		default:
			t.Fatalf("an accepted %s must reset the meter loop", key)
		}
	}
}

func TestClockAlignedDataIntervalIsValidated(t *testing.T) {
	st := quietStation(t, Config{ID: "SIM-CFG-03"}, "")
	if got := configValue(t, st, "ClockAlignedDataInterval"); got != "0" {
		t.Fatalf("clock-aligned data is off by default, got %s", got)
	}
	for _, tc := range []struct{ value, status, stored string }{
		{"900", "Accepted", "900"},
		{"0", "Accepted", "0"},
		{"-900", "Rejected", "0"},
		{"15m", "Rejected", "0"},
	} {
		if got := st.changeConfiguration("ClockAlignedDataInterval", tc.value); got != tc.status {
			t.Fatalf("ClockAlignedDataInterval=%q: status %s, want %s", tc.value, got, tc.status)
		}
		if got := configValue(t, st, "ClockAlignedDataInterval"); got != tc.stored {
			t.Fatalf("after %q the station reports %s, want %s", tc.value, got, tc.stored)
		}
	}
}

func TestMeterValuesSampledDataAcceptsOnlyWhatTheStationMeasures(t *testing.T) {
	ac := quietStation(t, Config{ID: "SIM-CFG-AC", Phases: 3}, "")
	dc := quietStation(t, Config{ID: "SIM-CFG-DC", DC: true}, "")
	for _, tc := range []struct {
		name   string
		st     *Station
		value  string
		status string
		stored string
	}{
		{"AC energy and power", ac, "Energy.Active.Import.Register,Power.Active.Import", "Accepted",
			"Energy.Active.Import.Register,Power.Active.Import"},
		{"AC cannot know the SoC", ac, "Energy.Active.Import.Register,Power.Active.Import,SoC", "Rejected",
			"Energy.Active.Import.Register,Power.Active.Import"},
		{"DC with SoC", dc, "Energy.Active.Import.Register,Power.Active.Import,SoC", "Accepted",
			"Energy.Active.Import.Register,Power.Active.Import,SoC"},
		{"spaces and duplicates", dc, " Power.Active.Import , SoC,Power.Active.Import", "Accepted",
			"Power.Active.Import,SoC"},
		{"a measurand the station does not have", dc, "Energy.Active.Import.Register,Temperature", "Rejected",
			"Power.Active.Import,SoC"},
		{"not a measurand", dc, "Energy", "Rejected", "Power.Active.Import,SoC"},
		{"empty item", dc, "SoC,", "Rejected", "Power.Active.Import,SoC"},
		{"empty list", dc, "", "Rejected", "Power.Active.Import,SoC"},
	} {
		if got := tc.st.changeConfiguration("MeterValuesSampledData", tc.value); got != tc.status {
			t.Fatalf("%s: status %s, want %s", tc.name, got, tc.status)
		}
		if got := configValue(t, tc.st, "MeterValuesSampledData"); got != tc.stored {
			t.Fatalf("%s: the station reports %q, want %q", tc.name, got, tc.stored)
		}
	}
}

func TestUnknownKeyIsNotSupported(t *testing.T) {
	st := quietStation(t, Config{ID: "SIM-CFG-04"}, "")
	if got := st.changeConfiguration("LightIntensity", "50"); got != "NotSupported" {
		t.Fatalf("an unknown key is NotSupported, got %s", got)
	}
}

func TestGetConfigurationReportsTheMeteringKeys(t *testing.T) {
	st := quietStation(t, Config{ID: "SIM-CFG-05", DC: true, MeterValuesS: 15, Connectors: 2}, "")
	want := map[string]string{
		"MeterValueSampleInterval": "15",
		"MeterValuesSampledData":   "Energy.Active.Import.Register,Power.Active.Import,SoC,Voltage,Current.Import",
		"ClockAlignedDataInterval": "0",
		"MeterValuesAlignedData":   "Energy.Active.Import.Register",
		"NumberOfConnectors":       "2",
	}
	got := map[string]string{}
	for _, kv := range st.getConfiguration(nil).ConfigurationKey {
		got[kv.Key] = *kv.Value
		if kv.Readonly != (kv.Key == "NumberOfConnectors") {
			t.Fatalf("%s: readonly=%v", kv.Key, kv.Readonly)
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}

	// The values the CSMS sets are the values it reads back.
	for key, value := range map[string]string{
		"MeterValueSampleInterval": "30",
		"MeterValuesSampledData":   "Energy.Active.Import.Register,Power.Active.Import,SoC",
		"ClockAlignedDataInterval": "900",
	} {
		if st.changeConfiguration(key, value) != "Accepted" {
			t.Fatalf("%s=%s must be accepted", key, value)
		}
		if got := configValue(t, st, key); got != value {
			t.Fatalf("%s reads back %q, want %q", key, got, value)
		}
	}

	conf := st.getConfiguration([]string{"ClockAlignedDataInterval", "NoSuchKey"})
	if len(conf.ConfigurationKey) != 1 || conf.ConfigurationKey[0].Key != "ClockAlignedDataInterval" ||
		!reflect.DeepEqual(conf.UnknownKey, []string{"NoSuchKey"}) {
		t.Fatalf("asking for keys returns those keys and lists the unknown ones: %+v", conf)
	}
}

func TestSampleKeepsOnlyTheConfiguredMeasurands(t *testing.T) {
	dc := &Config{DC: true, PowerKw: 50}
	ac := &Config{PowerKw: 22, Phases: 3}
	session := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 80, MaxAcKw: 7.4, MaxDcKw: 90}}
	measurands := func(mv ocpp.MeterValue) []string {
		var out []string
		for _, v := range mv.SampledValue {
			out = append(out, v.Measurand)
		}
		return out
	}

	two := []string{measurandEnergy, measurandPower}
	if got := measurands(session.Sample(dc, timeNow(), contextPeriodic, two)); !reflect.DeepEqual(got, two) {
		t.Fatalf("DC without SoC requested: %v", got)
	}
	withSoC := []string{measurandEnergy, measurandPower, measurandSoC}
	if got := measurands(session.Sample(dc, timeNow(), contextPeriodic, withSoC)); !reflect.DeepEqual(got, withSoC) {
		t.Fatalf("DC with SoC requested: %v", got)
	}
	if mv := session.Sample(ac, timeNow(), contextPeriodic, withSoC); hasMeasurand(mv, measurandSoC) {
		t.Fatal("AC never reports SoC, even when asked")
	}
	for _, v := range session.Sample(dc, timeNow(), contextClock, withSoC).SampledValue {
		if v.Context != contextClock {
			t.Fatalf("every value carries the reading context, got %q", v.Context)
		}
	}
}

func TestNextClockBoundaryStartsAtMidnight(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 3, 10, h, m, s, 0, time.UTC) }
	for _, tc := range []struct {
		now      time.Time
		interval int
		want     time.Time
	}{
		{at(12, 0, 0), 900, at(12, 15, 0)}, // on a boundary: the NEXT one
		{at(12, 7, 31), 900, at(12, 15, 0)},
		{at(23, 59, 59), 900, at(24, 0, 0)},
		{at(0, 0, 0).Add(-time.Nanosecond), 900, at(0, 0, 0)},
		// 7 h does not divide a day: 21:00 is the last boundary, then midnight.
		{at(22, 0, 0), 7 * 3600, at(24, 0, 0)},
		{at(6, 59, 59), 7 * 3600, at(7, 0, 0)},
		// Boundaries are UTC whatever the zone of the input.
		{at(12, 7, 0).In(time.FixedZone("UTC-3", -3*3600)), 900, at(12, 15, 0)},
	} {
		if got := nextClockBoundary(tc.now, tc.interval); !got.Equal(tc.want) {
			t.Fatalf("next boundary after %s every %ds: %s, want %s", tc.now, tc.interval, got, tc.want)
		}
	}
}

// ---------------------------------------------------- end to end, live CSMS

// meterValuesSince decodes the MeterValues the CSMS received from index from on.
func meterValuesSince(t *testing.T, csms *fakeCSMS, from int) []ocpp.MeterValuesReq {
	t.Helper()
	var out []ocpp.MeterValuesReq
	all := csms.payloads("MeterValues")
	for _, raw := range all[min(from, len(all)):] {
		var mv ocpp.MeterValuesReq
		if err := json.Unmarshal(raw, &mv); err != nil {
			t.Fatal(err)
		}
		out = append(out, mv)
	}
	return out
}

func readingContext(mv ocpp.MeterValuesReq) string {
	return mv.MeterValue[0].SampledValue[0].Context
}

func countContext(t *testing.T, csms *fakeCSMS, ctx string) int {
	t.Helper()
	n := 0
	for _, mv := range meterValuesSince(t, csms, 0) {
		if readingContext(mv) == ctx {
			n++
		}
	}
	return n
}

func changeConfiguration(t *testing.T, csms *fakeCSMS, key, value string) string {
	t.Helper()
	var conf ocpp.ChangeConfigurationConf
	if err := json.Unmarshal(csms.call(t, "ChangeConfiguration", ocpp.ChangeConfigurationReq{Key: key, Value: value}), &conf); err != nil {
		t.Fatal(err)
	}
	return conf.Status
}

func waitWithin(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startedStation boots a station against a fresh fake CSMS and opens a
// transaction on connector 1.
func startedStation(t *testing.T, cfg Config, beforeStart func(*fakeCSMS)) (*Station, *fakeCSMS) {
	t.Helper()
	csms := &fakeCSMS{}
	server := httptest.NewServer(csms.handler(t))
	t.Cleanup(server.Close)
	st := quietStation(t, cfg, "ws"+strings.TrimPrefix(server.URL, "http"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go st.Run(ctx)
	waitFor(t, "boot", func() bool { return len(csms.payloads("BootNotification")) > 0 })
	if beforeStart != nil {
		beforeStart(csms)
	}
	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "tag", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "start", func() bool { return len(csms.payloads("StartTransaction")) == 1 })
	return st, csms
}

// TestSampleIntervalChangesLive: the CSMS changes MeterValueSampleInterval in
// the middle of a transaction and the cadence follows at once, with no
// reconnect; 0 stops periodic samples while the car keeps charging.
func TestSampleIntervalChangesLive(t *testing.T) {
	t.Parallel()
	st, csms := startedStation(t, Config{
		ID: "SIM-CFG-LIVE", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, Phases: 3, MeterValuesS: 3600,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 100, MaxAcKw: 7.4},
	}, nil)

	time.Sleep(1500 * time.Millisecond)
	if n := len(csms.payloads("MeterValues")); n != 0 {
		t.Fatalf("one sample per hour: nothing yet, got %d", n)
	}

	if got := changeConfiguration(t, csms, "MeterValueSampleInterval", "1"); got != "Accepted" {
		t.Fatalf("MeterValueSampleInterval=1: %s", got)
	}
	waitWithin(t, "samples at the new 1 s cadence", 4*time.Second, func() bool {
		return len(csms.payloads("MeterValues")) >= 3
	})
	for _, mv := range meterValuesSince(t, csms, 0) {
		if got := readingContext(mv); got != contextPeriodic {
			t.Fatalf("periodic samples are Sample.Periodic, got %q", got)
		}
	}

	if got := changeConfiguration(t, csms, "MeterValueSampleInterval", "-5"); got != "Rejected" {
		t.Fatalf("a negative interval is Rejected, got %s", got)
	}
	if got := changeConfiguration(t, csms, "MeterValueSampleInterval", "0"); got != "Accepted" {
		t.Fatalf("MeterValueSampleInterval=0: %s", got)
	}
	energy := func() int {
		conns := st.Snapshot()["connectors"].([]map[string]any)
		return conns[0]["session"].(map[string]any)["energyWh"].(int)
	}
	time.Sleep(200 * time.Millisecond) // a sample already on the wire may land
	silent := len(csms.payloads("MeterValues"))
	before := energy()
	time.Sleep(2500 * time.Millisecond)
	if n := len(csms.payloads("MeterValues")); n != silent {
		t.Fatalf("0 disables periodic samples: %d more arrived", n-silent)
	}
	if after := energy(); after <= before {
		t.Fatalf("the car keeps charging while sampling is off: %d Wh -> %d Wh", before, after)
	}

	if got := changeConfiguration(t, csms, "MeterValueSampleInterval", "1"); got != "Accepted" {
		t.Fatalf("MeterValueSampleInterval=1 again: %s", got)
	}
	waitWithin(t, "samples again", 3*time.Second, func() bool {
		return len(csms.payloads("MeterValues")) > silent
	})

	var conf ocpp.GetConfigurationConf
	if err := json.Unmarshal(csms.call(t, "GetConfiguration", ocpp.GetConfigurationReq{Key: []string{"MeterValueSampleInterval"}}), &conf); err != nil {
		t.Fatal(err)
	}
	if len(conf.ConfigurationKey) != 1 || *conf.ConfigurationKey[0].Value != "1" {
		t.Fatalf("GetConfiguration over the wire: %+v", conf)
	}
}

// TestSampledDataChoosesThePeriodicMeasurands: the sequence a CSMS sends
// after boot on a DC station, then a change mid-session that adds SoC.
func TestSampledDataChoosesThePeriodicMeasurands(t *testing.T) {
	t.Parallel()
	_, csms := startedStation(t, Config{
		ID: "SIM-CFG-DC", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 50, DC: true, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 80, MaxDcKw: 90},
	}, func(csms *fakeCSMS) {
		if got := changeConfiguration(t, csms, "MeterValuesSampledData", "Energy.Active.Import.Register,Power.Active.Import"); got != "Accepted" {
			t.Fatalf("MeterValuesSampledData: %s", got)
		}
		if got := changeConfiguration(t, csms, "MeterValuesSampledData", "Energy.Active.Import.Register,Temperature"); got != "Rejected" {
			t.Fatalf("an unknown measurand is Rejected, got %s", got)
		}
	})

	waitFor(t, "a sample", func() bool { return len(csms.payloads("MeterValues")) > 0 })
	for _, mv := range meterValuesSince(t, csms, 0) {
		var got []string
		for _, v := range mv.MeterValue[0].SampledValue {
			got = append(got, v.Measurand)
		}
		if !reflect.DeepEqual(got, []string{measurandEnergy, measurandPower}) {
			t.Fatalf("only the configured measurands, no SoC: %v", got)
		}
	}

	if got := changeConfiguration(t, csms, "MeterValuesSampledData", "Energy.Active.Import.Register,Power.Active.Import,SoC"); got != "Accepted" {
		t.Fatalf("adding SoC on DC: %s", got)
	}
	from := len(csms.payloads("MeterValues"))
	waitFor(t, "a sample with SoC", func() bool {
		for _, mv := range meterValuesSince(t, csms, from) {
			if hasMeasurand(mv.MeterValue[0], measurandSoC) {
				return true
			}
		}
		return false
	})
}

// TestACRefusesSoCOverTheWire: the same request on AC is Rejected and the
// samples keep coming without SoC.
func TestACRefusesSoCOverTheWire(t *testing.T) {
	t.Parallel()
	_, csms := startedStation(t, Config{
		ID: "SIM-CFG-AC", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, Phases: 3, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 100, MaxAcKw: 7.4},
	}, func(csms *fakeCSMS) {
		if got := changeConfiguration(t, csms, "MeterValuesSampledData", "Energy.Active.Import.Register,Power.Active.Import,SoC"); got != "Rejected" {
			t.Fatalf("SoC on AC must be Rejected, got %s", got)
		}
	})
	waitFor(t, "two samples", func() bool { return len(csms.payloads("MeterValues")) >= 2 })
	for _, mv := range meterValuesSince(t, csms, 0) {
		if hasMeasurand(mv.MeterValue[0], measurandSoC) {
			t.Fatal("an AC station never sends SoC")
		}
	}
}

// TestClockAlignedSamplesLandOnBoundaries: with ClockAlignedDataInterval set,
// a running transaction gets Sample.Clock MeterValues stamped on the wall-clock
// boundaries, on top of the periodic ones; none without a transaction, and
// none once the interval is back to 0.
func TestClockAlignedSamplesLandOnBoundaries(t *testing.T) {
	t.Parallel()
	const interval = 2
	_, csms := startedStation(t, Config{
		ID: "SIM-CFG-CLOCK", Vendor: "Test", Model: "TestBox", Connectors: 1,
		PowerKw: 22, Phases: 3, MeterValuesS: 1,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 20, TargetSoc: 100, MaxAcKw: 7.4},
	}, func(csms *fakeCSMS) {
		if got := changeConfiguration(t, csms, "ClockAlignedDataInterval", "2"); got != "Accepted" {
			t.Fatalf("ClockAlignedDataInterval: %s", got)
		}
		time.Sleep(interval*time.Second + 500*time.Millisecond)
		if n := len(csms.payloads("MeterValues")); n != 0 {
			t.Fatalf("no transaction, no clock-aligned data: got %d MeterValues", n)
		}
	})

	waitWithin(t, "two clock-aligned samples", 4*interval*time.Second, func() bool {
		return countContext(t, csms, contextClock) >= 2
	})
	seen := map[int64]bool{}
	for _, mv := range meterValuesSince(t, csms, 0) {
		if readingContext(mv) != contextClock {
			continue
		}
		ts, err := time.Parse(time.RFC3339, mv.MeterValue[0].Timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if ts.Unix()%interval != 0 {
			t.Fatalf("a clock-aligned sample off the boundary: %s", ts)
		}
		if seen[ts.Unix()] {
			t.Fatalf("boundary %s sent twice", ts)
		}
		seen[ts.Unix()] = true
		var got []string
		for _, v := range mv.MeterValue[0].SampledValue {
			got = append(got, v.Measurand)
		}
		if !reflect.DeepEqual(got, []string{measurandEnergy}) {
			t.Fatalf("clock-aligned data carries MeterValuesAlignedData (the register by default): %v", got)
		}
	}
	if countContext(t, csms, contextPeriodic) == 0 {
		t.Fatal("periodic samples keep flowing alongside the clock-aligned ones")
	}

	if got := changeConfiguration(t, csms, "ClockAlignedDataInterval", "0"); got != "Accepted" {
		t.Fatalf("ClockAlignedDataInterval=0: %s", got)
	}
	time.Sleep(200 * time.Millisecond)
	clock := countContext(t, csms, contextClock)
	time.Sleep(interval*time.Second + 500*time.Millisecond)
	if n := countContext(t, csms, contextClock); n != clock {
		t.Fatalf("0 disables clock-aligned data: %d more arrived", n-clock)
	}
}
