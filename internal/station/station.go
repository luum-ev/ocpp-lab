package station

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// Config is the declarative identity of one simulated charge point — the
// "noun" that comes from fleet.yaml. Runtime actions are methods.
type Config struct {
	ID         string  `yaml:"id"`
	Vendor     string  `yaml:"vendor"`
	Model      string  `yaml:"model"`
	Serial     string  `yaml:"serial"`
	Firmware   string  `yaml:"firmware"`
	Connectors int     `yaml:"connectors"`
	PowerKw    float64 `yaml:"powerKw"`
	DC         bool    `yaml:"dc"`
	// Phases matters for AC only (per-phase currents in MeterValues).
	Phases       int `yaml:"phases"`
	HeartbeatS   int `yaml:"heartbeatS"`
	MeterValuesS int `yaml:"meterValuesS"`
	// Battery is the default simulated EV plugged into this station.
	Battery EVBattery `yaml:"battery"`
}

// Station is one simulated charge point: a WebSocket client, its connectors,
// and the offline queue the spec demands (transaction-related messages are
// stored while disconnected and delivered on reconnect — losing them would
// hide exactly the bugs this tool exists to expose).
type Station struct {
	Config Config
	CSMS   string

	mu         sync.Mutex
	conn       *websocket.Conn
	connectors []*Connector
	online     bool
	// wantOnline is the operator's intent: Offline() flips it off, Online()
	// back on; the run loop reconnects only when it is true.
	wantOnline bool
	txSeq      int
	msgSeq     int
	// pending maps in-flight CALL ids to the action, so responses route.
	pending map[string]string
	// queue holds frames that MUST survive a disconnection (StopTransaction,
	// MeterValues) — flushed in order on reconnect, per spec.
	queue [][]byte
	// profileLimitW per connector id, set by SetChargingProfile.
	profileLimitW map[int]float64
	// pendingTap is an RFID tap waiting for its Authorize answer.
	pendingTap *pendingTap

	log *slog.Logger
}

func New(cfg Config, csms string, log *slog.Logger) *Station {
	if cfg.Connectors <= 0 {
		cfg.Connectors = 1
	}
	if cfg.HeartbeatS <= 0 {
		cfg.HeartbeatS = 300
	}
	if cfg.MeterValuesS <= 0 {
		cfg.MeterValuesS = 30
	}
	if cfg.Phases == 0 {
		cfg.Phases = 3
	}
	s := &Station{
		Config:        cfg,
		CSMS:          csms,
		wantOnline:    true,
		pending:       map[string]string{},
		profileLimitW: map[int]float64{},
		log:           log.With("station", cfg.ID),
	}
	for i := 1; i <= cfg.Connectors; i++ {
		s.connectors = append(s.connectors, &Connector{ID: i, State: Available})
	}
	return s
}

// Run is the station's life: connect, boot, then tick until ctx ends. It
// reconnects with backoff — a real charge point never gives up either.
//
// The physics ticker lives HERE, not inside the connection loop: charging is
// local, only the telemetry link is not. A car keeps taking energy while the
// CSMS is unreachable — the MeterValues just pile up in the offline queue and
// travel on reconnect, which is exactly the store-and-forward the spec asks
// for. Tying the meter to the socket would freeze the car whenever the
// network blinks, and every energy figure after an outage would be a lie.
func (s *Station) Run(ctx context.Context) {
	meter := time.NewTicker(time.Duration(s.Config.MeterValuesS) * time.Second)
	defer meter.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-meter.C:
				if err := s.tickSessions(); err != nil {
					s.log.Warn("session tick failed", "error", err)
				}
			}
		}
	}()

	backoff := time.Second
	for ctx.Err() == nil {
		if !s.isWantOnline() {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err := s.connectAndServe(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("connection lost", "error", err, "retryIn", backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *Station) connectAndServe(ctx context.Context) error {
	url := s.CSMS + "/" + s.Config.ID
	dialer := websocket.Dialer{Subprotocols: []string{"ocpp1.6"}, HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, url, http.Header{})
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	s.mu.Lock()
	s.conn = conn
	s.online = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.online = false
		s.conn = nil
		s.mu.Unlock()
		conn.Close()
	}()
	s.log.Info("connected", "csms", url)

	if err := s.sendCall("BootNotification", ocpp.BootNotificationReq{
		ChargePointVendor:       s.Config.Vendor,
		ChargePointModel:        s.Config.Model,
		ChargePointSerialNumber: s.Config.Serial,
		FirmwareVersion:         s.Config.Firmware,
	}); err != nil {
		return err
	}
	// Announce every connector — a real station reports its state on boot.
	s.mu.Lock()
	for _, c := range s.connectors {
		s.queueStatusLocked(c)
	}
	s.flushQueueLocked()
	s.mu.Unlock()

	heartbeat := time.NewTicker(time.Duration(s.Config.HeartbeatS) * time.Second)
	defer heartbeat.Stop()

	readErr := make(chan error, 1)
	frames := make(chan []byte, 16)
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			frames <- raw
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case raw := <-frames:
			if err := s.handleFrame(raw); err != nil {
				s.log.Warn("bad frame", "error", err)
			}
		case <-heartbeat.C:
			if err := s.sendCall("Heartbeat", ocpp.HeartbeatReq{}); err != nil {
				return err
			}
		}
	}
}

// ------------------------------------------------------------ actions (API)

// Plug connects the cable on a connector.
func (s *Station) Plug(connector int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if err := c.Plug(); err != nil {
		return err
	}
	s.queueStatusLocked(c)
	s.flushQueueLocked()
	return nil
}

// Unplug removes the cable.
func (s *Station) Unplug(connector int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if err := c.Unplug(); err != nil {
		return err
	}
	s.queueStatusLocked(c)
	s.flushQueueLocked()
	return nil
}

// StartCharge begins a local transaction (the driver authorized at the
// station — the remote path arrives via RemoteStartTransaction instead).
func (s *Station) StartCharge(connector int, idTag string, battery *EVBattery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startChargeLocked(connector, idTag, battery)
}

func (s *Station) startChargeLocked(connector int, idTag string, battery *EVBattery) error {
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if !c.Plugged {
		return fmt.Errorf("connector %d: no cable — plug first (the spec's ConnectionTimeOut exists for a reason)", connector)
	}
	if c.Session != nil {
		return fmt.Errorf("connector %d: transaction already running", connector)
	}
	b := s.Config.Battery
	if battery != nil {
		b = *battery
	}
	if b.TargetSoc == 0 {
		b.TargetSoc = 100
	}
	s.txSeq++
	c.Session = &Session{
		IDTag:        idTag,
		StartedAt:    time.Now(),
		MeterStartWh: c.MeterWh,
		Battery:      b,
	}
	if limit, ok := s.profileLimitW[connector]; ok {
		c.Session.ProfileLimitW = limit
	}
	c.State = Charging
	s.queueCallLocked("StartTransaction", ocpp.StartTransactionReq{
		ConnectorID: connector,
		IDTag:       idTag,
		MeterStart:  c.MeterWh,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})
	s.queueStatusLocked(c)
	s.flushQueueLocked()
	return nil
}

// TapRFID simulates a card tap: the station sends Authorize and, when the
// CSMS accepts AND a cable is plugged, starts the transaction — the exact
// sequence a real charge point performs. The authorization decision is
// NEVER local (a charge point that decides is a charge point that gives
// energy away); the CSMS answer arrives asynchronously, so the outcome is
// observed via the CSMS side or the station snapshot.
func (s *Station) TapRFID(connector int, idTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if !c.Plugged {
		return fmt.Errorf("connector %d: tap with no cable — plug first, like at a real station", connector)
	}
	if c.Session != nil {
		return fmt.Errorf("connector %d: transaction already running", connector)
	}
	s.queueCallLocked("Authorize", ocpp.AuthorizeReq{IDTag: idTag})
	s.flushQueueLocked()
	// The start follows the Authorize answer — handled in handleCallResult,
	// which needs to know a tap is pending for this connector.
	s.pendingTap = &pendingTap{Connector: connector, IDTag: idTag}
	return nil
}

// StopCharge ends the transaction with the given reason (Local, Remote,
// EVDisconnected...).
func (s *Station) StopCharge(connector int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopChargeLocked(connector, reason)
}

func (s *Station) stopChargeLocked(connector int, reason string) error {
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if c.Session == nil {
		return fmt.Errorf("connector %d: no transaction running", connector)
	}
	c.MeterWh = c.Session.MeterStartWh + int(c.Session.EnergyWh)
	s.queueCallLocked("StopTransaction", ocpp.StopTransactionReq{
		TransactionID: c.Session.TransactionID,
		IDTag:         c.Session.IDTag,
		MeterStop:     c.MeterWh,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Reason:        reason,
	})
	c.Session = nil
	c.State = Finishing
	s.queueStatusLocked(c)
	s.flushQueueLocked()
	return nil
}

// DisconnectEV simulates the driver unlocking the car and pulling the
// cable FROM THE VEHICLE SIDE — possible at any time on a real session.
// The charge point notices the pilot signal drop, stops the transaction
// with reason EVDisconnected and releases the plug.
func (s *Station) DisconnectEV(connector int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if !c.Plugged {
		return fmt.Errorf("connector %d: no cable plugged", connector)
	}
	if c.Session != nil {
		if err := s.stopChargeLocked(connector, "EVDisconnected"); err != nil {
			return err
		}
	}
	c.Plugged = false
	c.State = Available
	s.queueStatusLocked(c)
	s.flushQueueLocked()
	return nil
}

// Kill drops the TCP connection without a Close frame — the brutal power cut.
// The CSMS under test should notice via its own timeouts, not via a goodbye.
func (s *Station) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.UnderlyingConn().Close()
	}
}

// Offline takes the station off the network (and keeps it off until Online).
// Sessions keep running — that is the point: energy flows, messages queue.
func (s *Station) Offline() {
	s.mu.Lock()
	s.wantOnline = false
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.UnderlyingConn().Close()
	}
}

// Online lets the station reconnect and flush its queue.
func (s *Station) Online() {
	s.mu.Lock()
	s.wantOnline = true
	s.mu.Unlock()
}

// Fault forces a connector into Faulted with the given OCPP error code.
func (s *Station) Fault(connector int, errorCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connectorLocked(connector)
	if err != nil {
		return err
	}
	if c.Session != nil {
		_ = s.stopChargeLocked(connector, "Other")
	}
	c.State = Faulted
	s.queueCallLocked("StatusNotification", ocpp.StatusNotificationReq{
		ConnectorID: c.ID, ErrorCode: errorCode, Status: string(Faulted),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	s.flushQueueLocked()
	return nil
}

// Snapshot returns the station state for the API.
func (s *Station) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	conns := make([]map[string]any, 0, len(s.connectors))
	for _, c := range s.connectors {
		entry := map[string]any{
			"id": c.ID, "state": c.State, "plugged": c.Plugged, "meterWh": c.MeterWh,
		}
		if c.Session != nil {
			entry["session"] = map[string]any{
				"transactionId": c.Session.TransactionID,
				"energyWh":      int(c.Session.EnergyWh),
				"socPercent":    c.Session.Battery.SocPercent,
				"dcReportsSoc":  s.Config.DC,
			}
		}
		conns = append(conns, entry)
	}
	return map[string]any{
		"id": s.Config.ID, "model": s.Config.Model, "dc": s.Config.DC,
		"powerKw": s.Config.PowerKw, "online": s.online, "queued": len(s.queue),
		"connectors": conns,
	}
}

// --------------------------------------------------------------- internals

func (s *Station) isWantOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wantOnline
}

func (s *Station) connectorLocked(id int) (*Connector, error) {
	if id < 1 || id > len(s.connectors) {
		return nil, fmt.Errorf("connector %d does not exist (station has %d)", id, len(s.connectors))
	}
	return s.connectors[id-1], nil
}

func (s *Station) tickSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := time.Duration(s.Config.MeterValuesS) * time.Second
	for _, c := range s.connectors {
		if c.Session == nil {
			continue
		}
		done := c.Session.Advance(&s.Config, dt)
		tx := c.Session.TransactionID
		s.queueCallLocked("MeterValues", ocpp.MeterValuesReq{
			ConnectorID:   c.ID,
			TransactionID: &tx,
			MeterValue:    []ocpp.MeterValue{c.Session.Sample(&s.Config, time.Now())},
		})
		if done {
			// The car reached its target: a real EV suspends, the driver
			// eventually stops. We stop with the honest reason.
			_ = s.stopChargeLocked(c.ID, "EVDisconnected")
		}
	}
	s.flushQueueLocked()
	return nil
}

func (s *Station) sendCall(action string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queueCallLocked(action, payload)
	s.flushQueueLocked()
	return nil
}

func (s *Station) queueCallLocked(action string, payload any) {
	s.msgSeq++
	id := fmt.Sprintf("%s-%d-%d", s.Config.ID, time.Now().Unix(), s.msgSeq+rand.Intn(1000))
	raw, err := ocpp.EncodeCall(id, action, payload)
	if err != nil {
		s.log.Error("encode call", "action", action, "error", err)
		return
	}
	s.pending[id] = action
	s.queue = append(s.queue, raw)
}

func (s *Station) queueStatusLocked(c *Connector) {
	s.queueCallLocked("StatusNotification", ocpp.StatusNotificationReq{
		ConnectorID: c.ID,
		ErrorCode:   "NoError",
		Status:      string(c.State),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})
}

// flushQueueLocked delivers queued frames while connected. On failure the
// remainder stays queued — that is the store-and-forward the spec demands.
func (s *Station) flushQueueLocked() {
	if s.conn == nil || !s.online {
		return
	}
	for len(s.queue) > 0 {
		if err := s.conn.WriteMessage(websocket.TextMessage, s.queue[0]); err != nil {
			s.log.Warn("flush interrupted, keeping queue", "queued", len(s.queue))
			return
		}
		s.queue = s.queue[1:]
	}
}

func (s *Station) handleFrame(raw []byte) error {
	f, err := ocpp.Decode(raw)
	if err != nil {
		return err
	}
	switch f.Type {
	case ocpp.CallResult:
		return s.handleCallResult(f)
	case ocpp.Call:
		return s.handleCall(f)
	case ocpp.CallError:
		s.mu.Lock()
		action := s.pending[f.MessageID]
		delete(s.pending, f.MessageID)
		s.mu.Unlock()
		s.log.Warn("CSMS returned CALLERROR", "action", action, "code", f.ErrorCode)
		return nil
	}
	return nil
}

func (s *Station) handleCallResult(f ocpp.Frame) error {
	s.mu.Lock()
	action := s.pending[f.MessageID]
	delete(s.pending, f.MessageID)
	s.mu.Unlock()
	switch action {
	case "StartTransaction":
		var conf ocpp.StartTransactionConf
		if err := json.Unmarshal(f.Payload, &conf); err != nil {
			return err
		}
		s.mu.Lock()
		// Attach the CSMS-issued transactionId to the newest session
		// still waiting for one.
		for _, c := range s.connectors {
			if c.Session != nil && c.Session.TransactionID == 0 {
				c.Session.TransactionID = conf.TransactionID
				break
			}
		}
		s.mu.Unlock()
	case "BootNotification":
		var conf ocpp.BootNotificationConf
		if err := json.Unmarshal(f.Payload, &conf); err != nil {
			return err
		}
		if conf.Interval > 0 {
			s.log.Info("boot accepted", "status", conf.Status, "heartbeatS", conf.Interval)
		}
	case "Authorize":
		var conf ocpp.AuthorizeConf
		if err := json.Unmarshal(f.Payload, &conf); err != nil {
			return err
		}
		s.mu.Lock()
		tap := s.pendingTap
		s.pendingTap = nil
		if tap != nil {
			if conf.IDTagInfo.Status == "Accepted" {
				s.log.Info("rfid tap authorized — starting", "idTag", tap.IDTag)
				if err := s.startChargeLocked(tap.Connector, tap.IDTag, nil); err != nil {
					s.log.Warn("tap start failed", "error", err)
				}
			} else {
				s.log.Info("rfid tap refused by CSMS", "idTag", tap.IDTag, "status", conf.IDTagInfo.Status)
			}
		}
		s.mu.Unlock()
	}
	return nil
}

// handleCall answers CSMS-initiated actions — the commands your platform
// sends and needs to see obeyed.
func (s *Station) handleCall(f ocpp.Frame) error {
	respond := func(payload any) error {
		raw, err := ocpp.EncodeCallResult(f.MessageID, payload)
		if err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conn == nil {
			return nil
		}
		return s.conn.WriteMessage(websocket.TextMessage, raw)
	}

	switch f.Action {
	case "RemoteStartTransaction":
		var req ocpp.RemoteStartTransactionReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		connector := 1
		if req.ConnectorID != nil {
			connector = *req.ConnectorID
		}
		s.mu.Lock()
		err := s.startChargeLocked(connector, req.IDTag, nil)
		s.mu.Unlock()
		status := "Accepted"
		if err != nil {
			status = "Rejected"
			s.log.Info("remote start rejected", "reason", err)
		}
		return respond(ocpp.RemoteStartStopConf{Status: status})

	case "RemoteStopTransaction":
		var req ocpp.RemoteStopTransactionReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		s.mu.Lock()
		status := "Rejected"
		for _, c := range s.connectors {
			if c.Session != nil && c.Session.TransactionID == req.TransactionID {
				_ = s.stopChargeLocked(c.ID, "Remote")
				status = "Accepted"
				break
			}
		}
		s.mu.Unlock()
		return respond(ocpp.RemoteStartStopConf{Status: status})

	case "Reset":
		if err := respond(ocpp.ResetConf{Status: "Accepted"}); err != nil {
			return err
		}
		// A reset is a reconnect: drop everything, running sessions stop
		// with the spec's reason, and the boot sequence replays.
		s.mu.Lock()
		for _, c := range s.connectors {
			if c.Session != nil {
				_ = s.stopChargeLocked(c.ID, "Reboot")
			}
		}
		conn := s.conn
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return nil

	case "UnlockConnector":
		var req ocpp.UnlockConnectorReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		s.mu.Lock()
		c, err := s.connectorLocked(req.ConnectorID)
		status := "Unlocked"
		if err != nil {
			status = "UnlockFailed"
		} else {
			if c.Session != nil {
				_ = s.stopChargeLocked(c.ID, "UnlockCommand")
			}
			c.Plugged = false
			c.State = Available
			s.queueStatusLocked(c)
			s.flushQueueLocked()
		}
		s.mu.Unlock()
		return respond(ocpp.UnlockConnectorConf{Status: status})

	case "ChangeAvailability":
		var req ocpp.ChangeAvailabilityReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		s.mu.Lock()
		target := Unavailable
		if req.Type == "Operative" {
			target = Available
		}
		if req.ConnectorID == 0 {
			for _, c := range s.connectors {
				if c.Session == nil {
					c.State = target
					s.queueStatusLocked(c)
				}
			}
		} else if c, err := s.connectorLocked(req.ConnectorID); err == nil && c.Session == nil {
			c.State = target
			s.queueStatusLocked(c)
		}
		s.flushQueueLocked()
		s.mu.Unlock()
		return respond(ocpp.ChangeAvailabilityConf{Status: "Accepted"})

	case "SetChargingProfile":
		var req ocpp.SetChargingProfileReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		// Obey the first period's limit — the emulator follows profiles, it
		// never computes them. W is used as-is; A converts at 230 V/phase.
		limit := 0.0
		if len(req.CsChargingProfiles.ChargingSchedule.ChargingSchedulePeriod) > 0 {
			limit = req.CsChargingProfiles.ChargingSchedule.ChargingSchedulePeriod[0].Limit
			if req.CsChargingProfiles.ChargingSchedule.ChargingRateUnit == "A" {
				phases := float64(s.Config.Phases)
				if s.Config.DC {
					phases = 1
				}
				limit *= 230 * phases
			}
		}
		s.mu.Lock()
		s.profileLimitW[req.ConnectorID] = limit
		if c, err := s.connectorLocked(req.ConnectorID); err == nil && c.Session != nil {
			c.Session.ProfileLimitW = limit
		}
		s.mu.Unlock()
		s.log.Info("charging profile applied", "connector", req.ConnectorID, "limitW", limit)
		return respond(ocpp.SetChargingProfileConf{Status: "Accepted"})

	case "ClearChargingProfile":
		s.mu.Lock()
		s.profileLimitW = map[int]float64{}
		for _, c := range s.connectors {
			if c.Session != nil {
				c.Session.ProfileLimitW = 0
			}
		}
		s.mu.Unlock()
		return respond(ocpp.ClearChargingProfileConf{Status: "Accepted"})

	case "GetConfiguration":
		hb := fmt.Sprintf("%d", s.Config.HeartbeatS)
		mv := fmt.Sprintf("%d", s.Config.MeterValuesS)
		return respond(ocpp.GetConfigurationConf{ConfigurationKey: []ocpp.KeyValue{
			{Key: "HeartbeatInterval", Readonly: false, Value: &hb},
			{Key: "MeterValueSampleInterval", Readonly: false, Value: &mv},
			{Key: "NumberOfConnectors", Readonly: true, Value: ptr(fmt.Sprintf("%d", s.Config.Connectors))},
		}})

	case "ChangeConfiguration":
		var req ocpp.ChangeConfigurationReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		status := "Accepted"
		switch req.Key {
		case "HeartbeatInterval":
			fmt.Sscanf(req.Value, "%d", &s.Config.HeartbeatS)
		case "MeterValueSampleInterval":
			fmt.Sscanf(req.Value, "%d", &s.Config.MeterValuesS)
		default:
			status = "NotSupported"
		}
		return respond(ocpp.ChangeConfigurationConf{Status: status})

	case "TriggerMessage":
		var req ocpp.TriggerMessageReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return err
		}
		if err := respond(ocpp.TriggerMessageConf{Status: "Accepted"}); err != nil {
			return err
		}
		switch req.RequestedMessage {
		case "Heartbeat":
			return s.sendCall("Heartbeat", ocpp.HeartbeatReq{})
		case "StatusNotification":
			s.mu.Lock()
			for _, c := range s.connectors {
				if req.ConnectorID == nil || *req.ConnectorID == c.ID {
					s.queueStatusLocked(c)
				}
			}
			s.flushQueueLocked()
			s.mu.Unlock()
		case "BootNotification":
			return s.sendCall("BootNotification", ocpp.BootNotificationReq{
				ChargePointVendor: s.Config.Vendor, ChargePointModel: s.Config.Model,
			})
		}
		return nil
	}

	raw, err := ocpp.EncodeCallError(f.MessageID, "NotImplemented", fmt.Sprintf("action %q is not implemented in ocpp-lab v1", f.Action))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.WriteMessage(websocket.TextMessage, raw)
}

type pendingTap struct {
	Connector int
	IDTag     string
}

func ptr[T any](v T) *T { return &v }
