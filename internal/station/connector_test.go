package station

import "testing"

func TestConnectorLifecycle(t *testing.T) {
	c := &Connector{ID: 1, State: Available}
	if err := c.Plug(); err != nil {
		t.Fatal(err)
	}
	if c.State != Preparing {
		t.Fatalf("after plug: %s", c.State)
	}
	if err := c.Plug(); err == nil {
		t.Fatal("double plug must fail")
	}
	c.State = Charging
	if err := c.Unplug(); err == nil {
		t.Fatal("unplug while charging must fail — the cable is locked")
	}
	c.State = Finishing
	if err := c.Unplug(); err != nil {
		t.Fatal(err)
	}
	if c.State != Available || c.Plugged {
		t.Fatalf("after unplug: %s plugged=%v", c.State, c.Plugged)
	}
}

func TestDCSampleReportsSoCAndACDoesNot(t *testing.T) {
	dc := &Config{DC: true, PowerKw: 50, Phases: 1}
	ac := &Config{DC: false, PowerKw: 22, Phases: 3}
	session := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 80, MaxAcKw: 7.4, MaxDcKw: 90}}

	dcSample := session.Sample(dc, timeNow())
	if !hasMeasurand(dcSample, "SoC") {
		t.Fatal("DC must report SoC — CCS talks to the BMS")
	}
	acSample := session.Sample(ac, timeNow())
	if hasMeasurand(acSample, "SoC") {
		t.Fatal("AC must NOT report SoC — Type 2 has no data link to the car")
	}
	phases := 0
	for _, v := range acSample.SampledValue {
		if v.Measurand == "Current.Import" && v.Phase != "" {
			phases++
		}
	}
	if phases != 3 {
		t.Fatalf("three-phase AC must report 3 per-phase currents, got %d", phases)
	}
}

func TestDCTaperAbove80Percent(t *testing.T) {
	cfg := &Config{DC: true, PowerKw: 150}
	full := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, MaxDcKw: 90}}
	tapered := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 95, MaxDcKw: 90}}
	if full.powerW(cfg) <= tapered.powerW(cfg) {
		t.Fatalf("power must taper above 80%% SoC: %f vs %f", full.powerW(cfg), tapered.powerW(cfg))
	}
}

func TestChargingProfileCapsPower(t *testing.T) {
	cfg := &Config{DC: false, PowerKw: 22, Phases: 3}
	s := &Session{Battery: EVBattery{CapacityKwh: 60, SocPercent: 40, MaxAcKw: 11}}
	free := s.powerW(cfg)
	s.ProfileLimitW = 3600
	if got := s.powerW(cfg); got != 3600 {
		t.Fatalf("SetChargingProfile limit must win: got %f, free was %f", got, free)
	}
}
