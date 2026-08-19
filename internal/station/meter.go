package station

import (
	"fmt"
	"math"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// EVBattery models the vehicle on the other side of the cable. It exists
// because a charging curve without a battery is a straight line — and straight
// lines don't test anything: DC power tapers as SoC climbs, sessions end when
// the target SoC is reached, and only DC stations can SEE any of this.
type EVBattery struct {
	CapacityKwh float64 `yaml:"capacityKwh" json:"capacityKwh"`
	SocPercent  float64 `yaml:"socPercent" json:"socPercent"`
	TargetSoc   float64 `yaml:"targetSoc" json:"targetSoc"`
	// MaxAcKw is the car's onboard charger limit — the reason a 22 kW AC
	// station often delivers 7.4 kW: the bottleneck is in the car.
	MaxAcKw float64 `yaml:"maxAcKw" json:"maxAcKw"`
	// MaxDcKw is the car's DC acceptance limit at low SoC.
	MaxDcKw float64 `yaml:"maxDcKw" json:"maxDcKw"`
}

// Session is one charging transaction in progress.
type Session struct {
	TransactionID int
	IDTag         string
	StartedAt     time.Time
	MeterStartWh  int
	EnergyWh      float64
	Battery       EVBattery
	// ProfileLimitW is the cap imposed by the CSMS via SetChargingProfile
	// (load balancing under test). Zero means no profile.
	ProfileLimitW float64
}

// powerW computes the instantaneous power for this tick — the minimum of what
// the station offers, what the car accepts at its current SoC, and what the
// CSMS allowed via charging profile.
func (s *Session) powerW(st *Config) float64 {
	station := st.PowerKw * 1000
	var car float64
	if st.DC {
		car = s.Battery.MaxDcKw * 1000
		// DC taper: full acceptance up to ~80% SoC, then a linear ramp down
		// to ~15% of acceptance at 100% — the shape every CCS session has.
		if s.Battery.SocPercent > 80 {
			frac := (100 - s.Battery.SocPercent) / 20 // 1.0 at 80%, 0 at 100%
			car *= math.Max(0.15, frac)
		}
	} else {
		car = s.Battery.MaxAcKw * 1000
	}
	p := math.Min(station, car)
	if s.ProfileLimitW > 0 {
		p = math.Min(p, s.ProfileLimitW)
	}
	return p
}

// Advance moves the session forward by dt and reports whether the target SoC
// was reached (the car stops asking for energy).
func (s *Session) Advance(st *Config, dt time.Duration) (done bool) {
	p := s.powerW(st)
	wh := p * dt.Hours()
	s.EnergyWh += wh
	if s.Battery.CapacityKwh > 0 {
		s.Battery.SocPercent += wh / (s.Battery.CapacityKwh * 1000) * 100
		if s.Battery.SocPercent >= s.Battery.TargetSoc {
			s.Battery.SocPercent = s.Battery.TargetSoc
			return true
		}
	}
	return false
}

// Sample builds the MeterValues payload for the current instant — and this is
// where AC and DC honestly differ: AC reports per-phase currents and NO SoC
// (a Type 2 cable has no data link to the car); DC reports SoC, DC voltage
// and DC current, because CCS talks to the BMS.
func (s *Session) Sample(st *Config, now time.Time) ocpp.MeterValue {
	p := s.powerW(st)
	register := float64(s.MeterStartWh) + s.EnergyWh
	values := []ocpp.SampledValue{
		{
			Value:     fmt.Sprintf("%.0f", register),
			Context:   "Sample.Periodic",
			Measurand: "Energy.Active.Import.Register",
			Unit:      "Wh",
		},
		{
			Value:     fmt.Sprintf("%.0f", p),
			Context:   "Sample.Periodic",
			Measurand: "Power.Active.Import",
			Unit:      "W",
		},
	}
	if st.DC {
		voltage := 400.0 // typical CCS pack voltage for the simulated fleet
		values = append(values,
			ocpp.SampledValue{
				Value:     fmt.Sprintf("%.1f", s.Battery.SocPercent),
				Context:   "Sample.Periodic",
				Measurand: "SoC",
				Unit:      "Percent",
			},
			ocpp.SampledValue{
				Value:     fmt.Sprintf("%.0f", voltage),
				Context:   "Sample.Periodic",
				Measurand: "Voltage",
				Unit:      "V",
			},
			ocpp.SampledValue{
				Value:     fmt.Sprintf("%.1f", p/voltage),
				Context:   "Sample.Periodic",
				Measurand: "Current.Import",
				Unit:      "A",
			},
		)
	} else {
		// 230 V per phase; three-phase stations split the current evenly.
		phases := []string{"L1"}
		if st.Phases == 3 {
			phases = []string{"L1", "L2", "L3"}
		}
		perPhase := p / (230.0 * float64(len(phases)))
		for _, ph := range phases {
			values = append(values, ocpp.SampledValue{
				Value:     fmt.Sprintf("%.1f", perPhase),
				Context:   "Sample.Periodic",
				Measurand: "Current.Import",
				Phase:     ph,
				Unit:      "A",
			})
		}
	}
	return ocpp.MeterValue{Timestamp: now.UTC().Format(time.RFC3339), SampledValue: values}
}
