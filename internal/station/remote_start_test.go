package station

import (
	"testing"
	"time"
)

// A real station accepts a remote start without a cable and waits for the
// plug (ConnectionTimeOut). The Luum app teaches "tap Carregar, then plug";
// an emulator that refused it rejected the flow it exists to test (PLUUM-452).
func TestRemoteStartWithoutCableWaitsAndStartsAtThePlug(t *testing.T) {
	st := stationForReservation(t)
	st.mu.Lock()
	err := st.remoteStartLocked(1, "TAG-MORADOR")
	c := st.connectors[0]
	state, waiting := c.State, c.WaitingStart != nil
	st.mu.Unlock()
	if err != nil {
		t.Fatalf("remote start without cable must be accepted: %v", err)
	}
	if state != Preparing || !waiting {
		t.Fatalf("waiting for the cable: want Preparing with a waiting start, got %s waiting=%v", state, waiting)
	}
	if err := st.Plug(1); err != nil {
		t.Fatalf("plugging a connector waiting for the cable: %v", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if c.Session == nil || c.Session.IDTag != "TAG-MORADOR" {
		t.Fatalf("the plug must start the transaction with the CSMS tag, got %+v", c.Session)
	}
	if c.WaitingStart != nil {
		t.Error("the waiting start must be consumed by the plug")
	}
}

// Nobody plugged within ConnectionTimeOut: back to Available, like a real one.
func TestRemoteStartWithoutCableExpires(t *testing.T) {
	st := stationForReservation(t)
	if got := st.changeConfiguration("ConnectionTimeOut", "1"); got != "Accepted" {
		t.Fatalf("ConnectionTimeOut must be configurable, got %s", got)
	}
	st.mu.Lock()
	if err := st.remoteStartLocked(1, "TAG-MORADOR"); err != nil {
		st.mu.Unlock()
		t.Fatal(err)
	}
	st.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		c := st.connectors[0]
		done := c.WaitingStart == nil && c.State == Available
		st.mu.Unlock()
		if done {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the waiting start did not expire back to Available")
}

// A second remote start while one waits is refused, not stacked.
func TestSecondRemoteStartWhileWaitingIsRefused(t *testing.T) {
	st := stationForReservation(t)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.remoteStartLocked(1, "A"); err != nil {
		t.Fatal(err)
	}
	if err := st.remoteStartLocked(1, "B"); err == nil {
		t.Fatal("a second remote start while one waits must be refused")
	}
}

// The armed refusal rejects exactly one remote start and leaves the
// connector Available; the next one is accepted (PLUUM-511, the "charger
// that refuses" of the dev end-to-end script).
func TestRefuseNextRemoteStartIsOneShot(t *testing.T) {
	st := stationForReservation(t)
	if err := st.RefuseNextRemoteStart(1); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.remoteStartLocked(1, "A"); err == nil {
		t.Fatal("the armed remote start must be refused")
	}
	if st.connectors[0].State != Available {
		t.Fatalf("a refused remote start leaves the connector Available, got %s", st.connectors[0].State)
	}
	if err := st.remoteStartLocked(1, "A"); err != nil {
		t.Fatalf("the refusal is one-shot: %v", err)
	}
}
