package station

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// answerStart plays the CSMS answering the queued StartTransaction.
func answerStart(t *testing.T, st *Station, status string, txID int) {
	t.Helper()
	st.mu.Lock()
	var id string
	for k, action := range st.pending {
		if action == "StartTransaction" {
			id = k
		}
	}
	st.mu.Unlock()
	if id == "" {
		t.Fatal("no StartTransaction in flight")
	}
	raw := fmt.Sprintf(`[3,%q,{"idTagInfo":{"status":%q},"transactionId":%d}]`, id, status, txID)
	if err := st.handleFrame([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

// queuedStops are the StopTransaction payloads still waiting to be sent.
func queuedStops(t *testing.T, st *Station) []map[string]any {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []map[string]any
	for _, raw := range st.queue {
		if !strings.Contains(string(raw), `"StopTransaction"`) {
			continue
		}
		var f []json.RawMessage
		if err := json.Unmarshal(raw, &f); err != nil || len(f) != 4 {
			t.Fatalf("bad frame %s", raw)
		}
		var p map[string]any
		_ = json.Unmarshal(f[3], &p)
		out = append(out, p)
	}
	return out
}

func startCharging(t *testing.T, st *Station) {
	t.Helper()
	if err := st.Plug(1); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCharge(1, "TAG-RECUSADA", nil); err != nil {
		t.Fatal(err)
	}
}

// The CSMS refused the tag (PLUUM-511, LAB-ACA-B02 on 28/10: Invalid, tx 0):
// a real station stops at once with DeAuthorized (StopTransactionOnInvalidId)
// instead of charging a session the CSMS never opened.
func TestRefusedTagStopsTheTransaction(t *testing.T) {
	st := stationForReservation(t)
	startCharging(t, st)
	answerStart(t, st, "Invalid", 0)

	st.mu.Lock()
	c := st.connectors[0]
	session, state := c.Session, c.State
	st.mu.Unlock()
	if session != nil || state != Finishing {
		t.Fatalf("refused tag must end the transaction: session=%+v state=%s", session, state)
	}
	stops := queuedStops(t, st)
	if len(stops) != 1 || stops[0]["reason"] != "DeAuthorized" || stops[0]["transactionId"] != float64(0) {
		t.Fatalf("want one StopTransaction DeAuthorized for tx 0, got %v", stops)
	}
}

// Accepted keeps charging with the CSMS transactionId — the normal path.
func TestAcceptedTagKeepsCharging(t *testing.T) {
	st := stationForReservation(t)
	startCharging(t, st)
	answerStart(t, st, "Accepted", 19)

	st.mu.Lock()
	defer st.mu.Unlock()
	c := st.connectors[0]
	if c.Session == nil || c.Session.TransactionID != 19 || c.State != Charging {
		t.Fatalf("accepted tag must keep charging with tx 19: %+v %s", c.Session, c.State)
	}
	for _, raw := range st.queue {
		if strings.Contains(string(raw), `"StopTransaction"`) {
			t.Fatalf("accepted tag must not stop: %s", raw)
		}
	}
}

// What was queued while offline goes AFTER the BootNotification on reconnect
// (PLUUM-511: the gateway saw a StatusNotification ahead of the boot).
func TestBootGoesAheadOfTheOfflineQueue(t *testing.T) {
	st := stationForReservation(t)
	if err := st.Plug(1); err != nil { // offline: the status stays queued
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.queue) == 0 {
		t.Fatal("the plug while offline must queue a StatusNotification")
	}
	st.queueBootFirstLocked()
	if !strings.Contains(string(st.queue[0]), `"BootNotification"`) {
		t.Fatalf("first frame after reconnect must be the boot, got %s", st.queue[0])
	}
	if !strings.Contains(string(st.queue[1]), `"StatusNotification"`) {
		t.Fatalf("the offline queue must follow the boot, got %s", st.queue[1])
	}
}
