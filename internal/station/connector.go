package station

import (
	"fmt"
	"time"
)

// State is the OCPP 1.6 connector status. The emulator's state machine only
// produces transitions a real charge point could produce — the CSMS under
// test must never see an impossible sequence.
type State string

const (
	Available   State = "Available"
	Preparing   State = "Preparing"
	Charging    State = "Charging"
	SuspendedEV State = "SuspendedEV"
	Finishing   State = "Finishing"
	Faulted     State = "Faulted"
	Unavailable State = "Unavailable"
	// Reserved is what makes a reservation real. The CSMS can hold a record
	// of who reserved what, but only the station can turn that record into
	// exclusivity — and it keeps doing so while the network is down.
	Reserved State = "Reserved"
)

// Reservation is what the station itself remembers about a held connector.
//
// The station keeps its own copy on purpose: the whole point of ReserveNow is
// that the connector stays exclusive when the CSMS is unreachable. A
// reservation that lives only in the CSMS is a reservation that evaporates
// with the link.
type Reservation struct {
	ID        int
	IDTag     string
	ExpiresAt time.Time
}

// Connector is one plug of a station. All mutation goes through the station's
// event loop, so no locking here.
type Connector struct {
	ID      int
	State   State
	Plugged bool
	// Reservation is nil unless the connector is held for someone.
	Reservation *Reservation
	// Session is nil unless a transaction is running on this connector.
	Session *Session
	// MeterWh is the lifetime energy register, like a real meter: it only
	// ever grows, and survives sessions — meterStart/meterStop come from it.
	MeterWh int
}

// Plug simulates the cable being connected. Only valid when nothing is
// happening on the connector: a real station goes Available → Preparing.
//
// PLUGGING A RESERVED CONNECTOR IS ALLOWED, and that is not an oversight: a
// real station cannot stop someone from inserting a cable. What it refuses is
// the transaction, and refusing at the right step is the difference between
// emulating a station and emulating a wish.
func (c *Connector) Plug() error {
	if c.Plugged {
		return fmt.Errorf("connector %d: cable already plugged", c.ID)
	}
	if c.State != Available && c.State != Reserved {
		return fmt.Errorf("connector %d: cannot plug while %s", c.ID, c.State)
	}
	c.Plugged = true
	// A reserved connector stays Reserved with a cable in it: the CSMS must
	// not read Preparing and think the spot became a normal free spot.
	if c.State != Reserved {
		c.State = Preparing
	}
	return nil
}

// ReservationBlocks answers whether this idTag may start here.
//
// One place decides, because the answer is needed by the local start path and
// by the remote one, and two copies of an exclusivity rule is one copy too
// many. An expired reservation blocks nobody — the station expires its own,
// which is why `expiryDate` is mandatory.
func (c *Connector) ReservationBlocks(idTag string, now time.Time) bool {
	if c.Reservation == nil {
		return false
	}
	if !c.Reservation.ExpiresAt.After(now) {
		return false
	}
	return c.Reservation.IDTag != idTag
}

// Unplug simulates the cable being removed. Refused mid-charge — a real
// Type 2 cable is locked while charging; forcing it is a fault, not an action.
func (c *Connector) Unplug() error {
	if !c.Plugged {
		return fmt.Errorf("connector %d: no cable plugged", c.ID)
	}
	if c.State == Charging || c.State == SuspendedEV {
		return fmt.Errorf("connector %d: cable is locked while charging — stop the session first", c.ID)
	}
	c.Plugged = false
	// Unplugging does NOT release a reservation: the person who reserved may
	// be the one who just gave up on a badly seated cable, and dropping the
	// hold here would hand their spot to whoever is next in line.
	if c.Reservation != nil {
		c.State = Reserved
	} else {
		c.State = Available
	}
	return nil
}
