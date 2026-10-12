package fleet

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleFleetParsesCompletely loads the repository's own example file
// and asserts the DEEP fields arrived. This test exists because of a real
// bug: EVBattery had no yaml tags, the camelCase keys silently didn't bind,
// and every simulated car charged at 0 W with 0% SoC — unit tests passed
// because they build the structs in Go. Config parsing gets its own proof.
func TestExampleFleetParsesCompletely(t *testing.T) {
	path := filepath.Join("..", "..", "fleet.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fleet.example.yaml must exist at the repo root: %v", err)
	}
	fl, err := Load(path, "", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := fl.Station("SIM-DC-001")
	if !ok {
		t.Fatal("SIM-DC-001 missing from example fleet")
	}
	b := dc.Config.Battery
	if b.CapacityKwh == 0 || b.SocPercent == 0 || b.TargetSoc == 0 || b.MaxDcKw == 0 {
		t.Fatalf("battery fields did not bind from YAML: %+v", b)
	}
	if !dc.Config.DC || dc.Config.PowerKw == 0 {
		t.Fatalf("station fields did not bind: %+v", dc.Config)
	}
	ac, _ := fl.Station("SIM-AC-001")
	if ac.Config.DC || ac.Config.Phases != 3 {
		t.Fatalf("AC station fields did not bind: %+v", ac.Config)
	}
}

func TestCsmsOverrideWins(t *testing.T) {
	fl, err := Load(filepath.Join("..", "..", "fleet.example.yaml"), "ws://override:9999/ocpp", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if fl.CSMS != "ws://override:9999/ocpp" {
		t.Fatalf("csms override lost: %s", fl.CSMS)
	}
}

// TestShippedSampleIntervals pins the MeterValueSampleInterval the shipped
// fleets boot with — AC every 30 s, DC every 15 s — in the example file and in
// the Helm chart's default values. A CSMS may change it at runtime with
// ChangeConfiguration; these are only the starting values.
func TestShippedSampleIntervals(t *testing.T) {
	want := map[string]int{"SIM-AC-001": 30, "SIM-DC-001": 15}

	fl, err := Load(filepath.Join("..", "..", "fleet.example.yaml"), "", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for id, s := range want {
		st, ok := fl.Station(id)
		if !ok {
			t.Fatalf("%s missing from fleet.example.yaml", id)
		}
		if st.Config.MeterValuesS != s {
			t.Fatalf("fleet.example.yaml %s: meterValuesS %d, want %d", id, st.Config.MeterValuesS, s)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "ocpp-lab", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Fleet File `yaml:"fleet"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, st := range values.Fleet.Stations {
		got[st.ID] = st.MeterValuesS
	}
	for id, s := range want {
		if got[id] != s {
			t.Fatalf("Helm values.yaml %s: meterValuesS %d, want %d", id, got[id], s)
		}
	}
}
