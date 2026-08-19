package station

import "fmt"

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
)

// Connector is one plug of a station. All mutation goes through the station's
// event loop, so no locking here.
type Connector struct {
	ID      int
	State   State
	Plugged bool
	// Session is nil unless a transaction is running on this connector.
	Session *Session
	// MeterWh is the lifetime energy register, like a real meter: it only
	// ever grows, and survives sessions — meterStart/meterStop come from it.
	MeterWh int
}

// Plug simulates the cable being connected. Only valid when nothing is
// happening on the connector: a real station goes Available → Preparing.
func (c *Connector) Plug() error {
	if c.Plugged {
		return fmt.Errorf("connector %d: cable already plugged", c.ID)
	}
	if c.State != Available {
		return fmt.Errorf("connector %d: cannot plug while %s", c.ID, c.State)
	}
	c.Plugged = true
	c.State = Preparing
	return nil
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
	c.State = Available
	return nil
}
