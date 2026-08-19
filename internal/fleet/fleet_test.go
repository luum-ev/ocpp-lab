package fleet

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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
