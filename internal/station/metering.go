package station

import (
	"context"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// physicsStep is how often the simulated cars move forward. It is NOT the
// MeterValues cadence: the CSMS may change or disable sampling at any time,
// and the battery must keep charging, reach its target and free reservations
// whatever the CSMS asked to be told.
const physicsStep = time.Second

// Reading contexts used in MeterValues (OCPP 1.6 ReadingContext enum).
const (
	contextPeriodic = "Sample.Periodic"
	contextClock    = "Sample.Clock"
)

// meterLoop owns the three clocks of the station: the physics step, the
// periodic sampling ticker (MeterValueSampleInterval) and the clock-aligned
// timer (ClockAlignedDataInterval). It lives for the whole Run, not per
// connection — charging is local, only the telemetry link is not, so a
// sample taken while offline waits in the queue like any other.
func (s *Station) meterLoop(ctx context.Context) {
	physics := time.NewTicker(physicsStep)
	defer physics.Stop()

	// The sampling ticker starts stopped; apply() starts it when the interval
	// is above 0.
	sample := time.NewTicker(time.Hour)
	sample.Stop()
	defer sample.Stop()
	sampleS := 0

	var clock *time.Timer
	var clockC <-chan time.Time
	var boundary time.Time
	clockS := 0
	defer func() {
		if clock != nil {
			clock.Stop()
		}
	}()

	// apply re-reads both intervals and resets only the clock that changed,
	// so a new MeterValueSampleInterval takes effect without a restart and
	// without shifting the clock-aligned boundaries (and the other way round).
	apply := func() {
		s.mu.Lock()
		wantSample, wantClock := s.Config.MeterValuesS, s.clockAlignedS
		s.mu.Unlock()
		if wantSample != sampleS {
			sampleS = wantSample
			sample.Stop()
			drain(sample.C)
			if sampleS > 0 {
				sample.Reset(time.Duration(sampleS) * time.Second)
			}
		}
		if wantClock != clockS {
			clockS = wantClock
			if clock != nil {
				clock.Stop()
				clock, clockC = nil, nil
			}
			if clockS > 0 {
				now := time.Now()
				boundary = nextClockBoundary(now, clockS)
				clock = time.NewTimer(boundary.Sub(now))
				clockC = clock.C
			}
		}
	}
	apply()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.meterConfig:
			apply()
		case now := <-physics.C:
			s.mu.Lock()
			s.stopFinishedLocked(s.physicsLocked(now))
			s.flushQueueLocked()
			s.mu.Unlock()
		case now := <-sample.C:
			s.mu.Lock()
			done := s.physicsLocked(now)
			s.sampleLocked(now, contextPeriodic, s.sampledData)
			s.stopFinishedLocked(done)
			s.flushQueueLocked()
			s.mu.Unlock()
		case <-clockC:
			// The reading is stamped with the boundary itself: that is what
			// "clock-aligned" means to the CSMS that bills on it.
			now := time.Now()
			s.mu.Lock()
			done := s.physicsLocked(now)
			s.sampleLocked(boundary, contextClock, s.alignedData)
			s.stopFinishedLocked(done)
			s.flushQueueLocked()
			s.mu.Unlock()
			// Never fire the same boundary twice, even if the wall clock
			// stepped back between the timer and time.Now().
			if now.Before(boundary) {
				now = boundary
			}
			next := nextClockBoundary(now, clockS)
			clock.Reset(time.Until(next))
			boundary = next
		}
	}
}

// drain empties a ticker channel after Stop, so a tick that was already
// buffered under the old interval is not delivered after the change.
func drain(c <-chan time.Time) {
	select {
	case <-c:
	default:
	}
}

// nextClockBoundary returns the first boundary strictly after now, where the
// boundaries are evenly spaced every intervalS seconds starting at 00:00:00
// UTC each day (OCPP 1.6: the clock-aligned intervals are a set per day
// starting at midnight). When intervalS does not divide a day, the last
// interval of the day is shorter and the next day starts again at midnight.
func nextClockBoundary(now time.Time, intervalS int) time.Time {
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	interval := time.Duration(intervalS) * time.Second
	elapsed := now.Sub(midnight)
	next := midnight.Add((elapsed/interval + 1) * interval)
	if tomorrow := midnight.AddDate(0, 0, 1); next.After(tomorrow) {
		return tomorrow
	}
	return next
}

// physicsLocked moves every running session forward to now and expires the
// reservations whose time is up. It returns the connectors whose car reached
// its target, so the caller can still sample them before the stop.
//
// Reservations expire on the physics step because expiry is time passing,
// exactly like energy. Putting it on the connection loop would mean a held
// connector never frees itself while offline, which is the one situation the
// station has to handle alone.
func (s *Station) physicsLocked(now time.Time) []int {
	s.expireReservationsLocked(now)
	var done []int
	for _, c := range s.connectors {
		if c.Session != nil && c.Session.AdvanceTo(&s.Config, now) {
			done = append(done, c.ID)
		}
	}
	return done
}

// advanceSessionsLocked brings every session up to now without ending any.
// It runs before anything that changes the power (suspend, a charging
// profile) or reads the register for good (a stop), so the energy delivered
// since the last physics step is counted at the power it was delivered at.
func (s *Station) advanceSessionsLocked(now time.Time) {
	for _, c := range s.connectors {
		if c.Session != nil {
			c.Session.AdvanceTo(&s.Config, now)
		}
	}
}

// stopFinishedLocked ends the sessions whose car reached its target: a real
// EV suspends, the driver eventually stops. We stop with the honest reason.
func (s *Station) stopFinishedLocked(connectors []int) {
	for _, id := range connectors {
		_ = s.stopChargeLocked(id, "EVDisconnected")
	}
}

// sampleLocked queues one MeterValues per running transaction, with the given
// reading context and only the measurands the CSMS configured. Periodic and
// clock-aligned data are transaction data, so they go through the offline
// queue like StopTransaction does.
func (s *Station) sampleLocked(at time.Time, readingContext string, measurands []string) {
	for _, c := range s.connectors {
		if c.Session == nil {
			continue
		}
		tx := c.Session.TransactionID
		s.queueCallLocked("MeterValues", ocpp.MeterValuesReq{
			ConnectorID:   c.ID,
			TransactionID: &tx,
			MeterValue:    []ocpp.MeterValue{c.Session.Sample(&s.Config, at, readingContext, measurands)},
		})
	}
}

// notifyMeterLoop tells the meter loop that an interval changed. It never
// blocks: one pending notification is enough, since the loop re-reads both
// intervals when it wakes up.
func (s *Station) notifyMeterLoop() {
	select {
	case s.meterConfig <- struct{}{}:
	default:
	}
}
