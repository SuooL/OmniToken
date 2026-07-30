package model

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

const (
	testDeviceID       = "550e8400-e29b-41d4-a716-446655440000"
	testBatchID        = "7d444840-9dc0-11d1-b245-5ffdce74fad2"
	testBootID         = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	testMaxBatchEvents = 2000
)

func validIngestEnvelope() IngestEnvelopeV2 {
	return IngestEnvelopeV2{
		ProtocolVersion: 2,
		DeviceID:        testDeviceID,
		BootID:          testBootID,
		BatchID:         testBatchID,
		Sequence:        42,
		CapturedAt:      1785319948062,
		Kind:            "events",
		Events: []Event{
			{EventID: "cc:message-1:request-1", Device: testDeviceID},
			{EventID: "cx:rollout-1", Device: testDeviceID},
		},
	}
}

func TestValidateIngestEnvelopeRejectsInvalidEnvelope(t *testing.T) {
	tooManyEvents := make([]Event, testMaxBatchEvents+1)
	for i := range tooManyEvents {
		tooManyEvents[i] = Event{
			EventID: "event-" + strconv.Itoa(i),
			Device:  testDeviceID,
		}
	}

	tests := []struct {
		name     string
		mutate   func(*IngestEnvelopeV2)
		wantCode string
	}{
		{
			name: "unsupported protocol version",
			mutate: func(e *IngestEnvelopeV2) {
				e.ProtocolVersion = 1
			},
			wantCode: "invalid_protocol_version",
		},
		{
			name: "malformed batch ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.BatchID = "not-a-uuid"
			},
			wantCode: "invalid_batch_id",
		},
		{
			name: "malformed device ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.DeviceID = "macmini"
			},
			wantCode: "invalid_device_id",
		},
		{
			name: "malformed boot ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.BootID = ""
			},
			wantCode: "invalid_boot_id",
		},
		{
			name: "missing sent timestamp",
			mutate: func(e *IngestEnvelopeV2) {
				e.CapturedAt = 0
			},
			wantCode: "invalid_captured_at",
		},
		{
			name: "sent timestamp exceeds JSON time range",
			mutate: func(e *IngestEnvelopeV2) {
				e.CapturedAt = 253402300800000
			},
			wantCode: "invalid_captured_at",
		},
		{
			name: "missing event ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.Events[0].EventID = ""
			},
			wantCode: "invalid_event_id",
		},
		{
			name: "duplicate event ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.Events[1].EventID = e.Events[0].EventID
			},
			wantCode: "duplicate_event_id",
		},
		{
			name: "event device differs from envelope",
			mutate: func(e *IngestEnvelopeV2) {
				e.Events[1].Device = "00000000-0000-4000-8000-000000000001"
			},
			wantCode: "event_device_mismatch",
		},
		{
			name: "batch exceeds maximum event count",
			mutate: func(e *IngestEnvelopeV2) {
				e.Events = tooManyEvents
			},
			wantCode: "batch_too_large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := validIngestEnvelope()
			tt.mutate(&envelope)

			got := ValidateIngestEnvelope(envelope)
			if len(got) != 1 {
				t.Fatalf("ValidateIngestEnvelope() returned %d rejections, want 1: %#v", len(got), got)
			}
			if got[0].Code != tt.wantCode {
				t.Errorf("rejection code = %q, want %q", got[0].Code, tt.wantCode)
			}
		})
	}
}

func TestValidateIngestEnvelopeAcceptsBatchSizeBoundary(t *testing.T) {
	envelope := validIngestEnvelope()
	envelope.Events = make([]Event, testMaxBatchEvents)
	for i := range envelope.Events {
		envelope.Events[i] = Event{
			EventID: "boundary-event-" + strconv.Itoa(i),
			Device:  testDeviceID,
		}
	}

	if got := ValidateIngestEnvelope(envelope); len(got) != 0 {
		t.Fatalf("ValidateIngestEnvelope() rejected a boundary-size batch: %#v", got)
	}
}

func TestValidateIngestEnvelopeAcceptsValidEnvelope(t *testing.T) {
	if got := ValidateIngestEnvelope(validIngestEnvelope()); len(got) != 0 {
		t.Fatalf("ValidateIngestEnvelope() rejected a valid envelope: %#v", got)
	}
}

func TestIngestProtocolTypesUseDocumentedJSONFields(t *testing.T) {
	envelopeJSON, err := json.Marshal(validIngestEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"protocol_version":`, `"device_id":`, `"boot_id":`, `"batch_id":`,
		`"sequence":`, `"captured_at":`, `"kind":`, `"events":`,
	} {
		if !strings.Contains(string(envelopeJSON), field) {
			t.Errorf("envelope JSON %s missing %s", envelopeJSON, field)
		}
	}

	ackJSON, err := json.Marshal(IngestAckV2{
		ProtocolVersion: 2,
		BatchID:         testBatchID,
		AckSequence:     42,
		Accepted:        2,
		Duplicates:      0,
		Rejected:        []IngestRejection{},
		ServerTime:      1785319949000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"protocol_version":`, `"batch_id":`, `"ack_sequence":`, `"accepted":`,
		`"duplicates":`, `"rejected":`, `"server_time":`,
	} {
		if !strings.Contains(string(ackJSON), field) {
			t.Errorf("ack JSON %s missing %s", ackJSON, field)
		}
	}
}
