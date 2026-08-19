package station

import (
	"time"

	"github.com/luum-ev/ocpp-lab/internal/ocpp"
)

func timeNow() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }

func hasMeasurand(mv ocpp.MeterValue, measurand string) bool {
	for _, v := range mv.SampledValue {
		if v.Measurand == measurand {
			return true
		}
	}
	return false
}
