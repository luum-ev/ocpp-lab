package ocpp

// The OCPP 1.6 payloads for the v1 message set. Shapes mirror the official
// JSON schemas: json tags use the spec's camelCase names, optional fields are
// pointers or omitempty. Nothing here is invented; extending the set means
// reading the schema first.

import "time"

// ---------------------------------------------------------------- outbound

// BootNotificationReq is sent once per connection, before anything else.
type BootNotificationReq struct {
	ChargePointVendor       string `json:"chargePointVendor"`
	ChargePointModel        string `json:"chargePointModel"`
	ChargePointSerialNumber string `json:"chargePointSerialNumber,omitempty"`
	FirmwareVersion         string `json:"firmwareVersion,omitempty"`
}

type BootNotificationConf struct {
	Status      string `json:"status"` // Accepted | Pending | Rejected
	CurrentTime string `json:"currentTime"`
	Interval    int    `json:"interval"` // heartbeat interval, seconds
}

type HeartbeatReq struct{}

type HeartbeatConf struct {
	CurrentTime string `json:"currentTime"`
}

// StatusNotificationReq reports a connector (or the station, connectorId 0)
// changing state. ErrorCode is mandatory in 1.6 — "NoError" when healthy.
type StatusNotificationReq struct {
	ConnectorID int    `json:"connectorId"`
	ErrorCode   string `json:"errorCode"`
	Status      string `json:"status"`
	Timestamp   string `json:"timestamp,omitempty"`
	Info        string `json:"info,omitempty"`
	VendorID    string `json:"vendorId,omitempty"`
}

type StatusNotificationConf struct{}

type AuthorizeReq struct {
	IDTag string `json:"idTag"`
}

type IDTagInfo struct {
	Status      string `json:"status"` // Accepted | Blocked | Expired | Invalid | ConcurrentTx
	ExpiryDate  string `json:"expiryDate,omitempty"`
	ParentIDTag string `json:"parentIdTag,omitempty"`
}

type AuthorizeConf struct {
	IDTagInfo IDTagInfo `json:"idTagInfo"`
}

type StartTransactionReq struct {
	ConnectorID   int    `json:"connectorId"`
	IDTag         string `json:"idTag"`
	MeterStart    int    `json:"meterStart"` // Wh
	Timestamp     string `json:"timestamp"`
	ReservationID *int   `json:"reservationId,omitempty"`
}

type StartTransactionConf struct {
	IDTagInfo     IDTagInfo `json:"idTagInfo"`
	TransactionID int       `json:"transactionId"`
}

type StopTransactionReq struct {
	TransactionID   int          `json:"transactionId"`
	IDTag           string       `json:"idTag,omitempty"`
	MeterStop       int          `json:"meterStop"` // Wh
	Timestamp       string       `json:"timestamp"`
	Reason          string       `json:"reason,omitempty"`
	TransactionData []MeterValue `json:"transactionData,omitempty"`
}

type StopTransactionConf struct {
	IDTagInfo *IDTagInfo `json:"idTagInfo,omitempty"`
}

// MeterValue and SampledValue are shared by MeterValues and transactionData.
type SampledValue struct {
	Value string `json:"value"`
	// Context: Sample.Periodic | Transaction.Begin | Transaction.End | Trigger ...
	Context   string `json:"context,omitempty"`
	Format    string `json:"format,omitempty"`
	Measurand string `json:"measurand,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Location  string `json:"location,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

type MeterValue struct {
	Timestamp    string         `json:"timestamp"`
	SampledValue []SampledValue `json:"sampledValue"`
}

type MeterValuesReq struct {
	ConnectorID   int          `json:"connectorId"`
	TransactionID *int         `json:"transactionId,omitempty"`
	MeterValue    []MeterValue `json:"meterValue"`
}

type MeterValuesConf struct{}

// ----------------------------------------------------------------- inbound

type RemoteStartTransactionReq struct {
	ConnectorID     *int             `json:"connectorId,omitempty"`
	IDTag           string           `json:"idTag"`
	ChargingProfile *ChargingProfile `json:"chargingProfile,omitempty"`
}

type RemoteStartStopConf struct {
	Status string `json:"status"` // Accepted | Rejected
}

type RemoteStopTransactionReq struct {
	TransactionID int `json:"transactionId"`
}

type ResetReq struct {
	Type string `json:"type"` // Hard | Soft
}

type ResetConf struct {
	Status string `json:"status"`
}

// ReserveNowReq is OCPP 1.6 §6.35. `expiryDate` and `reservationId` are
// required by the spec, and the emulator refuses the message without them —
// a CSMS that forgets the expiry would otherwise reserve a connector forever
// and only find out in the field.
type ReserveNowReq struct {
	ConnectorID int `json:"connectorId"`
	// ExpiryDate is RFC 3339. Zero value means the CSMS omitted it.
	ExpiryDate    time.Time `json:"expiryDate"`
	IDTag         string    `json:"idTag"`
	ParentIDTag   string    `json:"parentIdTag,omitempty"`
	ReservationID int       `json:"reservationId"`
}

// ReserveNowConf status set is the spec's, and every value is reachable in
// this emulator — a status a test can never observe teaches nothing.
type ReserveNowConf struct {
	// Accepted | Faulted | Occupied | Rejected | Unavailable
	Status string `json:"status"`
}

// CancelReservationReq is OCPP 1.6 §6.7 — the id alone, which is exactly why
// the CSMS must keep it unique among live reservations.
type CancelReservationReq struct {
	ReservationID int `json:"reservationId"`
}

type CancelReservationConf struct {
	Status string `json:"status"` // Accepted | Rejected
}

type UnlockConnectorReq struct {
	ConnectorID int `json:"connectorId"`
}

type UnlockConnectorConf struct {
	Status string `json:"status"` // Unlocked | UnlockFailed | NotSupported
}

type ChangeAvailabilityReq struct {
	ConnectorID int    `json:"connectorId"`
	Type        string `json:"type"` // Inoperative | Operative
}

type ChangeAvailabilityConf struct {
	Status string `json:"status"` // Accepted | Rejected | Scheduled
}

type TriggerMessageReq struct {
	RequestedMessage string `json:"requestedMessage"`
	ConnectorID      *int   `json:"connectorId,omitempty"`
}

type TriggerMessageConf struct {
	Status string `json:"status"` // Accepted | Rejected | NotImplemented
}

type GetConfigurationReq struct {
	Key []string `json:"key,omitempty"`
}

type KeyValue struct {
	Key      string  `json:"key"`
	Readonly bool    `json:"readonly"`
	Value    *string `json:"value,omitempty"`
}

type GetConfigurationConf struct {
	ConfigurationKey []KeyValue `json:"configurationKey,omitempty"`
	UnknownKey       []string   `json:"unknownKey,omitempty"`
}

type ChangeConfigurationReq struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ChangeConfigurationConf struct {
	Status string `json:"status"` // Accepted | Rejected | RebootRequired | NotSupported
}

// -------------------------------------------------------- charging profiles

type ChargingSchedulePeriod struct {
	StartPeriod  int     `json:"startPeriod"` // seconds from schedule start
	Limit        float64 `json:"limit"`       // in chargingRateUnit
	NumberPhases *int    `json:"numberPhases,omitempty"`
}

type ChargingSchedule struct {
	Duration               *int                     `json:"duration,omitempty"`
	StartSchedule          string                   `json:"startSchedule,omitempty"`
	ChargingRateUnit       string                   `json:"chargingRateUnit"` // W | A
	ChargingSchedulePeriod []ChargingSchedulePeriod `json:"chargingSchedulePeriod"`
	MinChargingRate        *float64                 `json:"minChargingRate,omitempty"`
}

type ChargingProfile struct {
	ChargingProfileID      int              `json:"chargingProfileId"`
	TransactionID          *int             `json:"transactionId,omitempty"`
	StackLevel             int              `json:"stackLevel"`
	ChargingProfilePurpose string           `json:"chargingProfilePurpose"` // ChargePointMaxProfile | TxDefaultProfile | TxProfile
	ChargingProfileKind    string           `json:"chargingProfileKind"`    // Absolute | Recurring | Relative
	RecurrencyKind         string           `json:"recurrencyKind,omitempty"`
	ValidFrom              string           `json:"validFrom,omitempty"`
	ValidTo                string           `json:"validTo,omitempty"`
	ChargingSchedule       ChargingSchedule `json:"chargingSchedule"`
}

type SetChargingProfileReq struct {
	ConnectorID        int             `json:"connectorId"`
	CsChargingProfiles ChargingProfile `json:"csChargingProfiles"`
}

type SetChargingProfileConf struct {
	Status string `json:"status"` // Accepted | Rejected | NotSupported
}

type ClearChargingProfileReq struct {
	ID                     *int   `json:"id,omitempty"`
	ConnectorID            *int   `json:"connectorId,omitempty"`
	ChargingProfilePurpose string `json:"chargingProfilePurpose,omitempty"`
	StackLevel             *int   `json:"stackLevel,omitempty"`
}

type ClearChargingProfileConf struct {
	Status string `json:"status"` // Accepted | Unknown
}
