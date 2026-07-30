package model

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	testDeviceID       = "550e8400-e29b-41d4-a716-446655440000"
	testBatchID        = "7d444840-9dc0-11d1-b245-5ffdce74fad2"
	testBootID         = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	testMaxBatchEvents = 2000
	testMaxCapturedAt  = int64(253402300799999)
	testMaxEnvelope    = 16 << 20
)

func TestIngestEnvelopeByteLimitProtocolContract(t *testing.T) {
	if MaxIngestEnvelopeBytes != testMaxEnvelope {
		t.Fatalf("MaxIngestEnvelopeBytes = %d, want %d", MaxIngestEnvelopeBytes, testMaxEnvelope)
	}
}

func validEnvelopeFields() IngestEnvelopeV2 {
	return IngestEnvelopeV2{
		ProtocolVersion: 2,
		DeviceID:        testDeviceID,
		BootID:          testBootID,
		BatchID:         testBatchID,
		Sequence:        42,
		CapturedAt:      1785319948062,
	}
}

func validIngestEnvelope() IngestEnvelopeV2 {
	envelope := validEnvelopeFields()
	envelope.Kind = "events"
	envelope.Events = []Event{
		{EventID: "cc:message-1:request-1", Device: testDeviceID},
		{EventID: "cx:rollout-1", Device: testDeviceID},
	}
	return envelope
}

func validQuotaEnvelope() IngestEnvelopeV2 {
	envelope := validEnvelopeFields()
	envelope.Kind = "quotas"
	envelope.Quotas = []QuotaSnapshot{{
		Device:        testDeviceID,
		Source:        "claude-code",
		LimitID:       "claude-account",
		Scope:         "five_hour",
		WindowMinutes: 300,
		UsedPercent:   42,
		ResetsAt:      1785320000000,
		ObservedAt:    1785319948062,
	}}
	return envelope
}

func validProcEnvelope() IngestEnvelopeV2 {
	envelope := validEnvelopeFields()
	envelope.Kind = "procs"
	envelope.Procs = &ProcReport{
		Device:     testDeviceID,
		ObservedAt: 1785319948062,
		Sessions: []ProcSession{{
			PID:       123,
			Source:    "codex",
			StartedAt: 1785319900000,
		}},
	}
	return envelope
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
			name: "negative sent timestamp",
			mutate: func(e *IngestEnvelopeV2) {
				e.CapturedAt = -1
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

func TestValidateIngestEnvelopeRequiresCanonicalUUIDs(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*IngestEnvelopeV2)
		wantCode string
	}{
		{
			name: "uppercase device ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.DeviceID = strings.ToUpper(testDeviceID)
			},
			wantCode: "invalid_device_id",
		},
		{
			name: "uppercase batch ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.BatchID = strings.ToUpper(testBatchID)
			},
			wantCode: "invalid_batch_id",
		},
		{
			name: "uppercase boot ID",
			mutate: func(e *IngestEnvelopeV2) {
				e.BootID = strings.ToUpper(testBootID)
			},
			wantCode: "invalid_boot_id",
		},
		{
			name: "nil device UUID",
			mutate: func(e *IngestEnvelopeV2) {
				e.DeviceID = "00000000-0000-0000-0000-000000000000"
			},
			wantCode: "invalid_device_id",
		},
		{
			name: "same-length device UUID with bad hyphen",
			mutate: func(e *IngestEnvelopeV2) {
				e.DeviceID = "550e8400_e29b-41d4-a716-446655440000"
			},
			wantCode: "invalid_device_id",
		},
		{
			name: "same-length device UUID with nonhex",
			mutate: func(e *IngestEnvelopeV2) {
				e.DeviceID = "550e840g-e29b-41d4-a716-446655440000"
			},
			wantCode: "invalid_device_id",
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

func TestValidateIngestEnvelopeEnforcesKindPayloadMatrix(t *testing.T) {
	tests := []struct {
		name     string
		envelope func() IngestEnvelopeV2
		wantCode string
	}{
		{
			name: "empty kind",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Kind = ""
				return e
			},
			wantCode: "invalid_kind",
		},
		{
			name: "unsupported kind",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Kind = "heartbeat"
				return e
			},
			wantCode: "invalid_kind",
		},
		{
			name: "events kind missing events",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Events = nil
				return e
			},
			wantCode: "missing_events_payload",
		},
		{
			name: "events kind carries quotas",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Quotas = validQuotaEnvelope().Quotas
				return e
			},
			wantCode: "unexpected_quotas_payload",
		},
		{
			name: "events kind carries procs",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Procs = validProcEnvelope().Procs
				return e
			},
			wantCode: "unexpected_procs_payload",
		},
		{
			name: "quotas kind missing quotas",
			envelope: func() IngestEnvelopeV2 {
				e := validQuotaEnvelope()
				e.Quotas = nil
				return e
			},
			wantCode: "missing_quotas_payload",
		},
		{
			name: "quotas kind carries events",
			envelope: func() IngestEnvelopeV2 {
				e := validQuotaEnvelope()
				e.Events = validIngestEnvelope().Events
				return e
			},
			wantCode: "unexpected_events_payload",
		},
		{
			name: "quotas kind carries procs",
			envelope: func() IngestEnvelopeV2 {
				e := validQuotaEnvelope()
				e.Procs = validProcEnvelope().Procs
				return e
			},
			wantCode: "unexpected_procs_payload",
		},
		{
			name: "procs kind missing procs",
			envelope: func() IngestEnvelopeV2 {
				e := validProcEnvelope()
				e.Procs = nil
				return e
			},
			wantCode: "missing_procs_payload",
		},
		{
			name: "procs kind carries events",
			envelope: func() IngestEnvelopeV2 {
				e := validProcEnvelope()
				e.Events = validIngestEnvelope().Events
				return e
			},
			wantCode: "unexpected_events_payload",
		},
		{
			name: "procs kind carries quotas",
			envelope: func() IngestEnvelopeV2 {
				e := validProcEnvelope()
				e.Quotas = validQuotaEnvelope().Quotas
				return e
			},
			wantCode: "unexpected_quotas_payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateIngestEnvelope(tt.envelope())
			if len(got) != 1 {
				t.Fatalf("ValidateIngestEnvelope() returned %d rejections, want 1: %#v", len(got), got)
			}
			if got[0].Code != tt.wantCode {
				t.Errorf("rejection code = %q, want %q", got[0].Code, tt.wantCode)
			}
		})
	}
}

func TestValidateIngestEnvelopeBindsEveryPayloadDevice(t *testing.T) {
	tests := []struct {
		name     string
		envelope func() IngestEnvelopeV2
		wantCode string
	}{
		{
			name: "event device",
			envelope: func() IngestEnvelopeV2 {
				e := validIngestEnvelope()
				e.Events[0].Device = strings.ToUpper(testDeviceID)
				return e
			},
			wantCode: "event_device_mismatch",
		},
		{
			name: "quota device",
			envelope: func() IngestEnvelopeV2 {
				e := validQuotaEnvelope()
				e.Quotas[0].Device = strings.ToUpper(testDeviceID)
				return e
			},
			wantCode: "quota_device_mismatch",
		},
		{
			name: "process device",
			envelope: func() IngestEnvelopeV2 {
				e := validProcEnvelope()
				e.Procs.Device = strings.ToUpper(testDeviceID)
				return e
			},
			wantCode: "proc_device_mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateIngestEnvelope(tt.envelope())
			if len(got) != 1 {
				t.Fatalf("ValidateIngestEnvelope() returned %d rejections, want 1: %#v", len(got), got)
			}
			if got[0].Code != tt.wantCode {
				t.Errorf("rejection code = %q, want %q", got[0].Code, tt.wantCode)
			}
		})
	}
}

func TestValidateIngestEnvelopeRejectsNonPositiveProcessObservedAt(t *testing.T) {
	for _, observedAt := range []int64{0, -1} {
		t.Run(strconv.FormatInt(observedAt, 10), func(t *testing.T) {
			envelope := validProcEnvelope()
			envelope.Procs.ObservedAt = observedAt

			got := ValidateIngestEnvelope(envelope)
			if len(got) != 1 {
				t.Fatalf("ValidateIngestEnvelope() returned %d rejections, want 1: %#v", len(got), got)
			}
			if got[0].Code != "invalid_proc_observed_at" {
				t.Errorf("rejection code = %q, want invalid_proc_observed_at", got[0].Code)
			}
		})
	}
}

func TestValidateIngestEnvelopeAcceptsBoundariesAndEveryKind(t *testing.T) {
	boundary := validIngestEnvelope()
	boundary.CapturedAt = testMaxCapturedAt
	boundary.Events = make([]Event, testMaxBatchEvents)
	for i := range boundary.Events {
		boundary.Events[i] = Event{
			EventID: "boundary-event-" + strconv.Itoa(i),
			Device:  testDeviceID,
		}
	}

	tests := []struct {
		name     string
		envelope IngestEnvelopeV2
	}{
		{name: "events at exact size and time limits", envelope: boundary},
		{name: "quota payload", envelope: validQuotaEnvelope()},
		{name: "process payload", envelope: validProcEnvelope()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateIngestEnvelope(tt.envelope); len(got) != 0 {
				t.Fatalf("ValidateIngestEnvelope() rejected a valid envelope: %#v", got)
			}
		})
	}
}

func TestValidateIngestEnvelopeReportsEventDetails(t *testing.T) {
	envelope := validIngestEnvelope()
	envelope.Events[1].EventID = envelope.Events[0].EventID

	got := ValidateIngestEnvelope(envelope)
	if len(got) != 1 {
		t.Fatalf("ValidateIngestEnvelope() returned %d rejections, want 1: %#v", len(got), got)
	}
	if got[0].Code != "duplicate_event_id" {
		t.Errorf("rejection code = %q, want duplicate_event_id", got[0].Code)
	}
	if got[0].Index == nil || *got[0].Index != 1 {
		t.Errorf("rejection index = %v, want 1", got[0].Index)
	}
	if got[0].EventID != "cc:message-1:request-1" {
		t.Errorf("rejection event_id = %q, want duplicate event ID", got[0].EventID)
	}
}

func TestValidateIngestEnvelopeOmissionAndErrorOrder(t *testing.T) {
	t.Run("omitted envelope", func(t *testing.T) {
		got := rejectionCodes(ValidateIngestEnvelope(IngestEnvelopeV2{}))
		want := []string{
			"invalid_protocol_version",
			"invalid_device_id",
			"invalid_boot_id",
			"invalid_batch_id",
			"invalid_captured_at",
			"invalid_kind",
		}
		assertStringsEqual(t, got, want)
	})

	t.Run("multiple errors preserve envelope then record order", func(t *testing.T) {
		envelope := validIngestEnvelope()
		envelope.ProtocolVersion = 1
		envelope.CapturedAt = 0
		envelope.Events[1].EventID = envelope.Events[0].EventID
		envelope.Events[1].Device = "00000000-0000-4000-8000-000000000001"

		got := rejectionCodes(ValidateIngestEnvelope(envelope))
		want := []string{
			"invalid_protocol_version",
			"invalid_captured_at",
			"duplicate_event_id",
			"event_device_mismatch",
		}
		assertStringsEqual(t, got, want)
	})
}

func TestIngestEnvelopeJSONContractAndOmission(t *testing.T) {
	tests := []struct {
		name     string
		envelope IngestEnvelopeV2
		wantKeys []string
	}{
		{
			name:     "events",
			envelope: validIngestEnvelope(),
			wantKeys: []string{
				"protocol_version", "device_id", "boot_id", "batch_id",
				"sequence", "captured_at", "kind", "events",
			},
		},
		{
			name:     "quotas",
			envelope: validQuotaEnvelope(),
			wantKeys: []string{
				"protocol_version", "device_id", "boot_id", "batch_id",
				"sequence", "captured_at", "kind", "quotas",
			},
		},
		{
			name:     "procs",
			envelope: validProcEnvelope(),
			wantKeys: []string{
				"protocol_version", "device_id", "boot_id", "batch_id",
				"sequence", "captured_at", "kind", "procs",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertJSONKeys(t, tt.envelope, tt.wantKeys)
		})
	}
}

func TestIngestAckJSONContract(t *testing.T) {
	assertJSONKeys(t, IngestAckV2{
		ProtocolVersion: 2,
		DeviceID:        testDeviceID,
		BatchID:         testBatchID,
		AckSequence:     42,
		Accepted:        2,
		Duplicates:      0,
		Rejected:        []IngestRejection{},
		ServerTime:      1785319949000,
	}, []string{
		"protocol_version", "device_id", "batch_id", "ack_sequence", "accepted",
		"duplicates", "rejected", "server_time",
	})
}

func TestHeartbeatJSONContract(t *testing.T) {
	assertJSONKeys(t, Heartbeat{
		ProtocolVersion: 2,
		DeviceID:        testDeviceID,
		AgentVersion:    "v2.0.0",
		BootID:          testBootID,
		Sequence:        43,
		SentAt:          1785319948062,
		Capabilities:    []string{"events", "procs"},
		QueuedBatches:   2,
		QueuedBytes:     4096,
		OldestQueuedAt:  1785319900000,
		LastScanAt:      1785319940000,
		LastUploadAt:    1785319930000,
		Warnings:        []string{"clock_skew"},
		ProcessState:    validProcEnvelope().Procs,
	}, []string{
		"protocol_version", "device_id", "agent_version", "boot_id", "sequence",
		"sent_at", "capabilities", "queued_batches", "queued_bytes",
		"oldest_queued_at", "last_scan_at", "last_upload_at", "warnings",
		"process_state",
	})
}

func TestIngestRejectionJSONDetails(t *testing.T) {
	index := 0
	assertJSONKeys(t, IngestRejection{
		Code:    "invalid_event_id",
		Index:   &index,
		EventID: "event-0",
	}, []string{"code", "index", "event_id"})
}

func rejectionCodes(rejections []IngestRejection) []string {
	out := make([]string, len(rejections))
	for i := range rejections {
		out[i] = rejections[i].Code
	}
	return out
}

func assertStringsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d values %v, want %d values %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value[%d] = %q, want %q; all got: %v", i, got[i], want[i], got)
		}
	}
}

func assertJSONKeys(t *testing.T, value any, want []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	assertStringsEqual(t, got, sortedWant)
}
