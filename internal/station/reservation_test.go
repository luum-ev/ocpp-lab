package station

import (
	"log/slog"
	"testing"
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// The reservation is only worth emulating if the emulator refuses what a real
// station refuses. Each test here corresponds to a way a CSMS gets this wrong.

func reservedConnector(idTag string, in time.Duration) *Connector {
	return &Connector{
		ID:          1,
		State:       Reserved,
		Reservation: &Reservation{ID: 7, IDTag: idTag, ExpiresAt: time.Now().Add(in)},
	}
}

func TestReservationBlocksOtherTagsAndLetsTheHolderIn(t *testing.T) {
	c := reservedConnector("TAG-MINE", 15*time.Minute)
	now := time.Now()

	if !c.ReservationBlocks("TAG-OTHER", now) {
		t.Error("a stranger must be blocked — otherwise the reservation is decoration")
	}
	if c.ReservationBlocks("TAG-MINE", now) {
		t.Error("the holder must get in; blocking them is worse than not reserving")
	}
}

// An expired hold blocks nobody. Without this the first reservation of the day
// would keep the connector for good — and the bug would surface only after the
// expiry passed, which is the worst time to find it.
func TestExpiredReservationBlocksNobody(t *testing.T) {
	c := reservedConnector("TAG-MINE", -time.Second)
	if c.ReservationBlocks("TAG-OTHER", time.Now()) {
		t.Error("an expired reservation must not block anyone")
	}
}

// A station cannot stop a hand from inserting a plug. What it refuses is the
// transaction — and refusing at the right step is the difference between
// emulating a station and emulating a wish.
func TestPluggingAReservedConnectorIsAllowedAndKeepsTheHold(t *testing.T) {
	c := reservedConnector("TAG-MINE", 15*time.Minute)
	if err := c.Plug(); err != nil {
		t.Fatalf("plugging a reserved connector must be allowed: %v", err)
	}
	if c.State != Reserved {
		t.Errorf("state after plug = %s, want Reserved — the CSMS must not read this spot as free", c.State)
	}
	if c.ReservationBlocks("TAG-OTHER", time.Now()) == false {
		t.Error("the hold must survive the cable going in")
	}
}

// Unplugging must NOT release the hold: the person who reserved may be the one
// who just gave up on a badly seated cable, and dropping the hold here would
// hand their spot to whoever is next.
func TestUnpluggingKeepsTheReservation(t *testing.T) {
	c := reservedConnector("TAG-MINE", 15*time.Minute)
	if err := c.Plug(); err != nil {
		t.Fatal(err)
	}
	if err := c.Unplug(); err != nil {
		t.Fatal(err)
	}
	if c.State != Reserved {
		t.Errorf("state after unplug = %s, want Reserved", c.State)
	}
	if c.Reservation == nil {
		t.Error("the reservation must survive an unplug")
	}
}

func stationForReservation(t *testing.T) *Station {
	t.Helper()
	/* No CSMS URL: these tests exercise the station's own decisions, and a
	   station that needs a socket to refuse a stranger would be a station that
	   stops refusing when the socket drops — which is the opposite of the
	   property being tested. */
	return New(Config{
		ID: "SIM-RES-01", Model: "Emulated", PowerKw: 22, Phases: 3,
		HeartbeatS: 60, MeterValuesS: 30, Connectors: 2,
		Battery: EVBattery{CapacityKwh: 60, SocPercent: 50, TargetSoc: 100, MaxAcKw: 7.4},
	}, "", slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})))
}

func reserve(t *testing.T, st *Station, connector, id int, idTag string, in time.Duration) string {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reserveLocked(ocpp.ReserveNowReq{
		ConnectorID: connector, IDTag: idTag, ReservationID: id,
		ExpiryDate: time.Now().Add(in),
	})
}

// The spec requires expiryDate. A station that accepted its absence would hold
// the connector forever, and the CSMS would find its own bug in the field.
func TestReserveNowRefusesAMissingOrPastExpiry(t *testing.T) {
	st := stationForReservation(t)

	st.mu.Lock()
	sem := st.reserveLocked(ocpp.ReserveNowReq{ConnectorID: 1, IDTag: "TAG", ReservationID: 1})
	st.mu.Unlock()
	if sem != "Rejected" {
		t.Errorf("missing expiryDate = %s, want Rejected", sem)
	}

	if got := reserve(t, st, 1, 2, "TAG", -time.Minute); got != "Rejected" {
		t.Errorf("expiry in the past = %s, want Rejected", got)
	}
	if got := reserve(t, st, 1, 3, "", time.Minute); got != "Rejected" {
		t.Errorf("empty idTag = %s, want Rejected", got)
	}
}

func TestReserveNowHoldsTheConnector(t *testing.T) {
	st := stationForReservation(t)
	if got := reserve(t, st, 1, 7, "TAG-MINE", 15*time.Minute); got != "Accepted" {
		t.Fatalf("reserve = %s, want Accepted", got)
	}
	st.mu.Lock()
	c, _ := st.connectorLocked(1)
	st.mu.Unlock()
	if c.State != Reserved {
		t.Errorf("state = %s, want Reserved", c.State)
	}
	if c.Reservation == nil || c.Reservation.ID != 7 {
		t.Fatalf("reservation = %+v, want id 7", c.Reservation)
	}
}

// Occupied is the answer that keeps the CSMS honest: reserving over a plugged
// connector would promise a spot that is taken right now.
func TestReserveNowIsOccupiedWhenTheCableIsIn(t *testing.T) {
	st := stationForReservation(t)
	st.mu.Lock()
	c, _ := st.connectorLocked(1)
	_ = c.Plug()
	st.mu.Unlock()

	if got := reserve(t, st, 1, 1, "TAG", 15*time.Minute); got != "Occupied" {
		t.Errorf("reserve over a plugged connector = %s, want Occupied", got)
	}
}

func TestReserveNowIsOccupiedForSomeoneElsesLiveHold(t *testing.T) {
	st := stationForReservation(t)
	if got := reserve(t, st, 1, 1, "TAG-A", 15*time.Minute); got != "Accepted" {
		t.Fatalf("first reserve = %s", got)
	}
	if got := reserve(t, st, 1, 2, "TAG-B", 15*time.Minute); got != "Occupied" {
		t.Errorf("second reserve = %s, want Occupied", got)
	}
}

// A retried command must not fail. Same reservationId is the CSMS repeating
// itself after a timeout, not a second person.
func TestReserveNowWithTheSameIdIsARetry(t *testing.T) {
	st := stationForReservation(t)
	if got := reserve(t, st, 1, 9, "TAG-A", 5*time.Minute); got != "Accepted" {
		t.Fatalf("first = %s", got)
	}
	if got := reserve(t, st, 1, 9, "TAG-A", 20*time.Minute); got != "Accepted" {
		t.Errorf("retry with the same id = %s, want Accepted", got)
	}
	st.mu.Lock()
	c, _ := st.connectorLocked(1)
	st.mu.Unlock()
	if time.Until(c.Reservation.ExpiresAt) < 10*time.Minute {
		t.Error("the retry should have extended the hold to the new expiry")
	}
}

func TestReserveNowReportsFaultedAndUnavailable(t *testing.T) {
	for state, want := range map[State]string{Faulted: "Faulted", Unavailable: "Unavailable"} {
		st := stationForReservation(t)
		st.mu.Lock()
		c, _ := st.connectorLocked(1)
		c.State = state
		st.mu.Unlock()
		if got := reserve(t, st, 1, 1, "TAG", time.Minute); got != want {
			t.Errorf("reserve while %s = %s, want %s", state, got, want)
		}
	}
}

// THE STATION EXPIRES ITS OWN HOLDS. This is the property that makes the
// feature survive an outage: if expiry needed a CancelReservation from the
// CSMS, a network partition would leave connectors held with nobody able to
// free them.
func TestTheStationExpiresItsOwnHolds(t *testing.T) {
	st := stationForReservation(t)
	if got := reserve(t, st, 1, 1, "TAG", time.Minute); got != "Accepted" {
		t.Fatalf("reserve = %s", got)
	}

	st.mu.Lock()
	st.expireReservationsLocked(time.Now().Add(2 * time.Minute))
	c, _ := st.connectorLocked(1)
	st.mu.Unlock()

	if c.Reservation != nil {
		t.Error("the hold should be gone after its expiry")
	}
	if c.State != Available {
		t.Errorf("state = %s, want Available", c.State)
	}
}

// The start path is where exclusivity actually bites — and both paths need it,
// because the tap path asks the CSMS through an Authorize that has no
// connectorId.
func TestStartIsRefusedForAStrangerOnAReservedConnector(t *testing.T) {
	st := stationForReservation(t)
	if got := reserve(t, st, 1, 1, "TAG-MINE", 15*time.Minute); got != "Accepted" {
		t.Fatalf("reserve = %s", got)
	}
	st.mu.Lock()
	c, _ := st.connectorLocked(1)
	_ = c.Plug()
	err := st.startChargeLocked(1, "TAG-OTHER", nil)
	st.mu.Unlock()
	if err == nil {
		t.Fatal("a stranger started a transaction on a reserved connector")
	}

	st.mu.Lock()
	errHolder := st.startChargeLocked(1, "TAG-MINE", nil)
	st.mu.Unlock()
	if errHolder != nil {
		t.Fatalf("the holder must be able to start: %v", errHolder)
	}
}
