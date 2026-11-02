package station

import (
	"math"
	"sort"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

/*
SMART CHARGING (OCPP 1.6 §3.13, SmartCharging feature profile).

The station OBEYS charging profiles; it never computes them. What it does with
the ones it holds, every physics step and on every Set/Clear:

 1. Per connector, the TxProfile of the running transaction wins over any
    TxDefaultProfile. Within a purpose the active profile with the highest
    stackLevel wins, and a profile installed on the connector itself wins over
    one installed on connectorId 0.
 2. The ChargePointMaxProfile (connectorId 0 only) caps the WHOLE station:
    its limit is shared among the connectors that are drawing energy.
 3. IEC 61851-1 floor: an AC car cannot charge below 6 A per phase. A
    connector whose share is under it gets ZERO (SuspendedEVSE) instead of a
    current the car would refuse, and resumes when the limit allows 6 A again.
    When a shared ChargePointMaxProfile cannot give everyone 6 A, the
    highest-numbered connectors wait and the others charge — a few cars
    charging beats every car starved.

A limit in A converts to W at 230 V per phase, with the period's numberPhases
(capped at the station's phases) or the station's phases when absent.
*/

// Nominal phase voltage and the IEC 61851-1 minimum AC charging current.
const (
	phaseVoltage = 230.0
	minChargingA = 6.0
)

// What the simulated firmware can hold, advertised in GetConfiguration
// (ChargeProfileMaxStackLevel, ChargingScheduleMaxPeriods,
// MaxChargingProfilesInstalled) and enforced on SetChargingProfile.
const (
	maxStackLevel        = 8
	maxSchedulePeriods   = 24
	maxProfilesInstalled = 16
)

// Charging profile purposes (OCPP 1.6 ChargingProfilePurposeType).
const (
	purposeChargePointMax = "ChargePointMaxProfile"
	purposeTxDefault      = "TxDefaultProfile"
	purposeTx             = "TxProfile"
)

// installedProfile is a profile the station accepted, with the connector it
// was installed on and the moment it arrived (the start of an Absolute
// schedule that has no startSchedule).
type installedProfile struct {
	connectorID int
	receivedAt  time.Time
	profile     ocpp.ChargingProfile
}

// connectorLimit is a power cap with the phases it was expressed for, so the
// 6 A floor is checked per phase and not against a three-phase total.
type connectorLimit struct {
	watts  float64
	phases int
}

// smartChargingEnabled reports whether this station implements the
// SmartCharging feature profile (the fleet file's `smartCharging`, default on).
func (c *Config) smartChargingEnabled() bool {
	return c.SmartCharging == nil || *c.SmartCharging
}

// setChargingProfileLocked validates and installs a profile, answering with
// the SetChargingProfile.conf status.
func (s *Station) setChargingProfileLocked(req ocpp.SetChargingProfileReq, now time.Time) string {
	p := req.CsChargingProfiles
	if req.ConnectorID < 0 || req.ConnectorID > len(s.connectors) {
		return "Rejected"
	}
	if !validSchedule(p) {
		return "Rejected"
	}
	switch p.ChargingProfilePurpose {
	case purposeChargePointMax:
		// The whole station's cap lives on connector 0 by definition.
		if req.ConnectorID != 0 {
			return "Rejected"
		}
	case purposeTxDefault:
	case purposeTx:
		// A TxProfile belongs to a running transaction (§3.13.2): without
		// one there is nothing for it to limit.
		if req.ConnectorID == 0 {
			return "Rejected"
		}
		c := s.connectors[req.ConnectorID-1]
		if c.Session == nil {
			return "Rejected"
		}
		if p.TransactionID != nil && c.Session.TransactionID != 0 && *p.TransactionID != c.Session.TransactionID {
			return "Rejected"
		}
	default:
		return "Rejected"
	}
	// Same id replaces; so does the same stackLevel + purpose on the same
	// connector (§5.16.3) — a CSMS re-sending its profile must not pile up.
	kept := make([]installedProfile, 0, len(s.profiles)+1)
	for _, ip := range s.profiles {
		same := ip.profile.ChargingProfileID == p.ChargingProfileID ||
			(ip.connectorID == req.ConnectorID && ip.profile.StackLevel == p.StackLevel &&
				ip.profile.ChargingProfilePurpose == p.ChargingProfilePurpose)
		if !same {
			kept = append(kept, ip)
		}
	}
	if len(kept) >= maxProfilesInstalled {
		return "Rejected"
	}
	s.profiles = append(kept, installedProfile{connectorID: req.ConnectorID, receivedAt: now, profile: p})
	return "Accepted"
}

// validSchedule is the shape check a real station does before accepting.
func validSchedule(p ocpp.ChargingProfile) bool {
	if p.StackLevel < 0 || p.StackLevel > maxStackLevel {
		return false
	}
	sch := p.ChargingSchedule
	if sch.ChargingRateUnit != "A" && sch.ChargingRateUnit != "W" {
		return false
	}
	periods := sch.ChargingSchedulePeriod
	// The first period starts the schedule (startPeriod 0, per the spec), and
	// the rest must come in order.
	if len(periods) == 0 || len(periods) > maxSchedulePeriods || periods[0].StartPeriod != 0 {
		return false
	}
	for i, pr := range periods {
		if pr.Limit < 0 || (i > 0 && pr.StartPeriod <= periods[i-1].StartPeriod) {
			return false
		}
	}
	switch p.ChargingProfileKind {
	case "Absolute", "Relative":
	case "Recurring":
		if p.RecurrencyKind != "Daily" && p.RecurrencyKind != "Weekly" {
			return false
		}
		if _, ok := parseTime(sch.StartSchedule); !ok {
			return false
		}
	default:
		return false
	}
	return true
}

// clearChargingProfileLocked removes the profiles matching the request and
// answers Accepted, or Unknown when nothing matched. With an id, only the id
// counts; otherwise every criterion given must match, and none clears all.
func (s *Station) clearChargingProfileLocked(req ocpp.ClearChargingProfileReq) string {
	removed := 0
	kept := s.profiles[:0]
	for _, ip := range s.profiles {
		var match bool
		if req.ID != nil {
			match = ip.profile.ChargingProfileID == *req.ID
		} else {
			match = (req.ConnectorID == nil || *req.ConnectorID == ip.connectorID) &&
				(req.ChargingProfilePurpose == "" || req.ChargingProfilePurpose == ip.profile.ChargingProfilePurpose) &&
				(req.StackLevel == nil || *req.StackLevel == ip.profile.StackLevel)
		}
		if match {
			removed++
		} else {
			kept = append(kept, ip)
		}
	}
	s.profiles = kept
	if removed == 0 {
		return "Unknown"
	}
	return "Accepted"
}

// dropTxProfilesLocked forgets the TxProfiles of a connector whose
// transaction ended: they were for that transaction only.
func (s *Station) dropTxProfilesLocked(connector int) {
	kept := s.profiles[:0]
	for _, ip := range s.profiles {
		if ip.connectorID == connector && ip.profile.ChargingProfilePurpose == purposeTx {
			continue
		}
		kept = append(kept, ip)
	}
	s.profiles = kept
}

// applyProfilesLocked recomputes every session's cap from the installed
// profiles at `now`, suspends or resumes at the 6 A floor, and queues a
// StatusNotification for every connector whose state changed.
func (s *Station) applyProfilesLocked(now time.Time) {
	limits := map[int]connectorLimit{}
	for _, c := range s.connectors {
		if c.Session == nil {
			continue
		}
		own, ok := s.purposeLimitLocked(purposeTx, c.ID, now, c.Session.StartedAt)
		if !ok {
			own, ok = s.purposeLimitLocked(purposeTxDefault, c.ID, now, c.Session.StartedAt)
		}
		if ok {
			limits[c.ID] = own
		}
	}
	phasesOf := func(id int) int {
		if l, ok := limits[id]; ok {
			return l.phases
		}
		return s.Config.Phases
	}
	underFloor := func(w float64, phases int) bool {
		if s.Config.DC {
			// The 6 A pilot floor is AC (IEC 61851-1); a DC charger only
			// stops at zero.
			return w <= 0
		}
		return w/(phaseVoltage*float64(phases)) < minChargingA-1e-9
	}

	// alloc is what each drawing connector may take; evse marks the ones the
	// station holds at zero.
	alloc := map[int]float64{}
	evse := map[int]bool{}
	var sharing []*Connector
	for _, c := range s.connectors {
		if c.Session == nil || c.Session.Suspended {
			continue
		}
		demand := c.Session.offeredW(&s.Config)
		if l, ok := limits[c.ID]; ok {
			if underFloor(l.watts, l.phases) {
				evse[c.ID] = true
				continue
			}
			demand = math.Min(demand, l.watts)
		}
		alloc[c.ID] = demand
		sharing = append(sharing, c)
	}
	if cp, ok := s.purposeLimitLocked(purposeChargePointMax, 0, now, time.Time{}); ok {
		// Water-fill the station cap; when it cannot give every car the
		// floor, the highest-numbered connector waits and the rest re-share.
		for len(sharing) > 0 {
			shares := waterFill(cp.watts, sharing, alloc)
			short := false
			for _, c := range sharing {
				if shares[c.ID] < alloc[c.ID] && underFloor(shares[c.ID], phasesOf(c.ID)) {
					short = true
				}
			}
			if !short {
				for id, w := range shares {
					alloc[id] = w
				}
				break
			}
			last := sharing[len(sharing)-1]
			evse[last.ID] = true
			sharing = sharing[:len(sharing)-1]
		}
	}

	for _, c := range s.connectors {
		sess := c.Session
		if sess == nil {
			continue
		}
		sess.EVSESuspended = evse[c.ID]
		sess.ProfileLimitW = 0
		if l, ok := limits[c.ID]; ok {
			sess.ProfileLimitW = l.watts
		}
		if w, ok := alloc[c.ID]; ok && w < sess.offeredW(&s.Config) {
			sess.ProfileLimitW = w
		}
		want := Charging
		switch {
		case sess.EVSESuspended:
			want = SuspendedEVSE
		case sess.Suspended:
			want = SuspendedEV
		}
		if c.State != want && (c.State == Charging || c.State == SuspendedEV || c.State == SuspendedEVSE) {
			c.State = want
			s.queueStatusLocked(c)
		}
	}
}

// waterFill shares capW among the connectors: each gets an equal share,
// capped at its own demand, and what a capped one leaves goes to the rest.
func waterFill(capW float64, cs []*Connector, demand map[int]float64) map[int]float64 {
	out := map[int]float64{}
	order := append([]*Connector(nil), cs...)
	sort.SliceStable(order, func(i, j int) bool { return demand[order[i].ID] < demand[order[j].ID] })
	left := math.Max(capW, 0)
	for i, c := range order {
		share := left / float64(len(order)-i)
		w := math.Min(share, demand[c.ID])
		out[c.ID] = w
		left -= w
	}
	return out
}

// purposeLimitLocked finds the limit of one purpose for a connector at now:
// the active profile with the highest stackLevel, preferring the ones on the
// connector itself over the ones on connector 0.
func (s *Station) purposeLimitLocked(purpose string, connector int, now, txStart time.Time) (connectorLimit, bool) {
	for _, onConnector := range []int{connector, 0} {
		best, found := connectorLimit{}, false
		bestLevel := -1
		for _, ip := range s.profiles {
			if ip.connectorID != onConnector || ip.profile.ChargingProfilePurpose != purpose {
				continue
			}
			if ip.profile.StackLevel <= bestLevel {
				continue
			}
			l, ok := s.profileLimitAt(ip, now, txStart)
			if !ok {
				continue
			}
			best, found, bestLevel = l, true, ip.profile.StackLevel
		}
		if found {
			return best, true
		}
		if onConnector == 0 {
			break
		}
	}
	return connectorLimit{}, false
}

// profileLimitAt returns the limit of the period active at now, in W, or
// false when the profile is not in force (outside validFrom/validTo, before
// its start, past its duration, or Relative with no transaction).
func (s *Station) profileLimitAt(ip installedProfile, now, txStart time.Time) (connectorLimit, bool) {
	p := ip.profile
	if from, ok := parseTime(p.ValidFrom); ok && now.Before(from) {
		return connectorLimit{}, false
	}
	if to, ok := parseTime(p.ValidTo); ok && !now.Before(to) {
		return connectorLimit{}, false
	}
	sch := p.ChargingSchedule
	var start time.Time
	switch p.ChargingProfileKind {
	case "Relative":
		if txStart.IsZero() {
			return connectorLimit{}, false
		}
		start = txStart
	case "Recurring":
		start, _ = parseTime(sch.StartSchedule)
		if now.Before(start) {
			return connectorLimit{}, false
		}
		cycle := 24 * time.Hour
		if p.RecurrencyKind == "Weekly" {
			cycle = 7 * 24 * time.Hour
		}
		start = start.Add(now.Sub(start) / cycle * cycle)
	default: // Absolute
		var ok bool
		if start, ok = parseTime(sch.StartSchedule); !ok {
			start = ip.receivedAt
		}
	}
	elapsed := now.Sub(start)
	if elapsed < 0 {
		return connectorLimit{}, false
	}
	if sch.Duration != nil && elapsed >= time.Duration(*sch.Duration)*time.Second {
		return connectorLimit{}, false
	}
	var period *ocpp.ChargingSchedulePeriod
	for i := range sch.ChargingSchedulePeriod {
		if time.Duration(sch.ChargingSchedulePeriod[i].StartPeriod)*time.Second <= elapsed {
			period = &sch.ChargingSchedulePeriod[i]
		}
	}
	if period == nil {
		return connectorLimit{}, false
	}
	phases := s.Config.Phases
	if period.NumberPhases != nil && *period.NumberPhases > 0 && *period.NumberPhases < phases {
		phases = *period.NumberPhases
	}
	return connectorLimit{watts: limitToW(period.Limit, sch.ChargingRateUnit, phases), phases: phases}, true
}

// limitToW converts a schedule limit to watts: W as-is, A at 230 V per phase.
func limitToW(limit float64, unit string, phases int) float64 {
	if unit == "A" {
		return limit * phaseVoltage * float64(phases)
	}
	return limit
}

func parseTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}
