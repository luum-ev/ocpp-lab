package ocpp

import (
	"encoding/json"
	"testing"
)

func TestCallRoundTrip(t *testing.T) {
	raw, err := EncodeCall("19223201", "BootNotification", BootNotificationReq{
		ChargePointVendor: "WEG", ChargePointModel: "WEMOB Parking 22",
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != Call || f.MessageID != "19223201" || f.Action != "BootNotification" {
		t.Fatalf("frame mismatch: %+v", f)
	}
	var req BootNotificationReq
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.ChargePointVendor != "WEG" {
		t.Fatalf("payload mismatch: %+v", req)
	}
}

// TestSpecExampleFrame decodes the CALL example from the OCPP 1.6J
// specification (section 4.2.1) — the wire format is not ours to reinvent.
func TestSpecExampleFrame(t *testing.T) {
	raw := []byte(`[2,"19223201","BootNotification",{"chargePointVendor":"VendorX","chargePointModel":"SingleSocketCharger"}]`)
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Action != "BootNotification" {
		t.Fatalf("action: %s", f.Action)
	}
}

func TestCallErrorShape(t *testing.T) {
	raw, err := EncodeCallError("42", "NotImplemented", "action Unknown is not implemented")
	if err != nil {
		t.Fatal(err)
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 5 {
		t.Fatalf("CALLERROR must have 5 elements, got %d", len(parts))
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, bad := range []string{`{}`, `[2,"x"]`, `[9,"x","y",{}]`, `not json`} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
