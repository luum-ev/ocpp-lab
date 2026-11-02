package station

import (
	"strconv"
	"strings"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

// The measurands this emulator can produce. Which of them a station HAS
// depends on its type: SoC exists only on DC (CCS talks to the BMS, a Type 2
// cable has no data link to the car), and only DC reports a pack voltage.
const (
	measurandEnergy  = "Energy.Active.Import.Register"
	measurandPower   = "Power.Active.Import"
	measurandCurrent = "Current.Import"
	measurandVoltage = "Voltage"
	measurandSoC     = "SoC"
)

// maxIntervalS caps both metering intervals at one day. ClockAlignedDataInterval
// is defined as evenly spaced intervals per day, and a sample interval above a
// day is a typo, not a configuration.
const maxIntervalS = 86400

// supportedMeasurands lists what a station of this type can measure, in the
// order the values appear in a MeterValue.
func supportedMeasurands(dc bool) []string {
	if dc {
		return []string{measurandEnergy, measurandPower, measurandSoC, measurandVoltage, measurandCurrent}
	}
	return []string{measurandEnergy, measurandPower, measurandCurrent}
}

// parseMeasurands reads a MeterValuesSampledData / MeterValuesAlignedData
// value: a comma-separated list of OCPP 1.6 measurands. Every item must be a
// measurand this station can produce. A list with one it cannot (SoC on AC,
// Temperature, a typo) is refused as a whole: accepting it would promise data
// that never arrives, and the CSMS would only find out from missing readings.
func parseMeasurands(value string, dc bool) ([]string, bool) {
	supported := map[string]bool{}
	for _, m := range supportedMeasurands(dc) {
		supported[m] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		m := strings.TrimSpace(item)
		if !supported[m] {
			return nil, false
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, len(out) > 0
}

// parseInterval reads an interval key in seconds: an integer from 0 to a day.
func parseInterval(value string) (int, bool) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > maxIntervalS {
		return 0, false
	}
	return n, true
}

// changeConfiguration answers ChangeConfiguration (OCPP 1.6 §5.3) with
// Accepted, Rejected or NotSupported. Every value is applied under the station
// lock, and the metering intervals take effect immediately: the meter loop is
// told to reset its ticker and its clock-aligned timer, no reboot needed.
func (s *Station) changeConfiguration(key, value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch key {
	case "HeartbeatInterval":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return "Rejected"
		}
		s.Config.HeartbeatS = n
	case "MeterValueSampleInterval":
		// 0 means no periodic sampled data (OCPP 1.6 §9.1, Core Profile keys).
		n, ok := parseInterval(value)
		if !ok {
			return "Rejected"
		}
		s.Config.MeterValuesS = n
		s.notifyMeterLoop()
	case "ClockAlignedDataInterval":
		// 0 means no clock-aligned data (OCPP 1.6 §9.1, Core Profile keys).
		n, ok := parseInterval(value)
		if !ok {
			return "Rejected"
		}
		s.clockAlignedS = n
		s.notifyMeterLoop()
	case "MeterValuesSampledData":
		m, ok := parseMeasurands(value, s.Config.DC)
		if !ok {
			return "Rejected"
		}
		s.sampledData = m
	case "MeterValuesAlignedData":
		m, ok := parseMeasurands(value, s.Config.DC)
		if !ok {
			return "Rejected"
		}
		s.alignedData = m
	case "ConnectionTimeOut":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 || n > maxIntervalS {
			return "Rejected"
		}
		s.connectionTimeoutS = n
	case "NumberOfConnectors", "SupportedFeatureProfiles":
		// A key the station knows but will not change is Rejected;
		// NotSupported is for keys it does not have at all.
		return "Rejected"
	case "ChargeProfileMaxStackLevel", "ChargingScheduleAllowedChargingRateUnit",
		"ChargingScheduleMaxPeriods", "MaxChargingProfilesInstalled":
		// The SmartCharging keys exist only on a station with the profile.
		if !s.Config.smartChargingEnabled() {
			return "NotSupported"
		}
		return "Rejected"
	default:
		return "NotSupported"
	}
	return "Accepted"
}

// getConfiguration answers GetConfiguration (OCPP 1.6 §5.8): every known key
// when none is asked for, otherwise the asked ones, with the rest reported as
// unknownKey.
func (s *Station) getConfiguration(keys []string) ocpp.GetConfigurationConf {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := []ocpp.KeyValue{
		{Key: "HeartbeatInterval", Value: ptr(strconv.Itoa(s.Config.HeartbeatS))},
		{Key: "MeterValueSampleInterval", Value: ptr(strconv.Itoa(s.Config.MeterValuesS))},
		{Key: "MeterValuesSampledData", Value: ptr(strings.Join(s.sampledData, ","))},
		{Key: "ClockAlignedDataInterval", Value: ptr(strconv.Itoa(s.clockAlignedS))},
		{Key: "MeterValuesAlignedData", Value: ptr(strings.Join(s.alignedData, ","))},
		{Key: "ConnectionTimeOut", Value: ptr(strconv.Itoa(s.connectionTimeoutS))},
		{Key: "NumberOfConnectors", Readonly: true, Value: ptr(strconv.Itoa(s.Config.Connectors))},
		{Key: "SupportedFeatureProfiles", Readonly: true, Value: ptr(strings.Join(s.featureProfiles(), ","))},
	}
	if s.Config.smartChargingEnabled() {
		// The SmartCharging profile's own keys (OCPP 1.6 §9.4), read-only:
		// they describe what this station's firmware can hold.
		all = append(all,
			ocpp.KeyValue{Key: "ChargeProfileMaxStackLevel", Readonly: true, Value: ptr(strconv.Itoa(maxStackLevel))},
			ocpp.KeyValue{Key: "ChargingScheduleAllowedChargingRateUnit", Readonly: true, Value: ptr("Current,Power")},
			ocpp.KeyValue{Key: "ChargingScheduleMaxPeriods", Readonly: true, Value: ptr(strconv.Itoa(maxSchedulePeriods))},
			ocpp.KeyValue{Key: "MaxChargingProfilesInstalled", Readonly: true, Value: ptr(strconv.Itoa(maxProfilesInstalled))},
		)
	}
	if len(keys) == 0 {
		return ocpp.GetConfigurationConf{ConfigurationKey: all}
	}
	byKey := map[string]ocpp.KeyValue{}
	for _, kv := range all {
		byKey[kv.Key] = kv
	}
	var conf ocpp.GetConfigurationConf
	for _, k := range keys {
		if kv, ok := byKey[k]; ok {
			conf.ConfigurationKey = append(conf.ConfigurationKey, kv)
		} else {
			conf.UnknownKey = append(conf.UnknownKey, k)
		}
	}
	return conf
}

// featureProfiles is the SupportedFeatureProfiles value: the OCPP 1.6
// feature profiles this station implements. Core always; Reservation
// (ReserveNow/CancelReservation) and RemoteTrigger (TriggerMessage) always;
// SmartCharging unless the fleet file turns it off. FirmwareManagement and
// LocalAuthListManagement are not implemented, so they are not claimed.
func (s *Station) featureProfiles() []string {
	out := []string{"Core", "Reservation", "RemoteTrigger"}
	if s.Config.smartChargingEnabled() {
		out = append(out, "SmartCharging")
	}
	return out
}
