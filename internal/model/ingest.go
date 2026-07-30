package model

const (
	// IngestProtocolV2 is the only protocol version accepted by the v2 ingest
	// endpoint.
	IngestProtocolV2 = 2

	// MaxIngestBatchEvents bounds one request independently of the HTTP body
	// limit. It matches the collector's existing sink batch size.
	MaxIngestBatchEvents = 2000

	// Last millisecond representable by the protocol's RFC 3339 date domain
	// (9999-12-31T23:59:59.999Z).
	maxCapturedAtMilli int64 = 253402300799999
)

const (
	IngestKindEvents = "events"
	IngestKindQuotas = "quotas"
	IngestKindProcs  = "procs"
)

// Stable machine-readable validation codes. Clients may persist these in a
// dead-letter record, so changing a value is a protocol change.
const (
	RejectionProtocolVersion     = "invalid_protocol_version"
	RejectionBatchID             = "invalid_batch_id"
	RejectionDeviceID            = "invalid_device_id"
	RejectionBootID              = "invalid_boot_id"
	RejectionCapturedAt          = "invalid_captured_at"
	RejectionBatchTooLarge       = "batch_too_large"
	RejectionEventID             = "invalid_event_id"
	RejectionDuplicateEventID    = "duplicate_event_id"
	RejectionEventDeviceMismatch = "event_device_mismatch"
	RejectionKind                = "invalid_kind"
	RejectionMissingEvents       = "missing_events_payload"
	RejectionMissingQuotas       = "missing_quotas_payload"
	RejectionMissingProcs        = "missing_procs_payload"
	RejectionUnexpectedEvents    = "unexpected_events_payload"
	RejectionUnexpectedQuotas    = "unexpected_quotas_payload"
	RejectionUnexpectedProcs     = "unexpected_procs_payload"
	RejectionQuotaDeviceMismatch = "quota_device_mismatch"
	RejectionProcDeviceMismatch  = "proc_device_mismatch"
)

// IngestEnvelopeV2 is one acknowledged edge-to-hub delivery batch.
type IngestEnvelopeV2 struct {
	ProtocolVersion int             `json:"protocol_version"`
	DeviceID        string          `json:"device_id"`
	BootID          string          `json:"boot_id"`
	BatchID         string          `json:"batch_id"`
	Sequence        uint64          `json:"sequence"`
	CapturedAt      int64           `json:"captured_at"`
	Kind            string          `json:"kind"`
	Events          []Event         `json:"events,omitempty"`
	Quotas          []QuotaSnapshot `json:"quotas,omitempty"`
	Procs           *ProcReport     `json:"procs,omitempty"`
}

// IngestAckV2 explicitly acknowledges a batch. An HTTP success status alone is
// not sufficient: uploaders must also match ProtocolVersion, DeviceID, BatchID,
// and AckSequence before deleting durable outbox data.
type IngestAckV2 struct {
	ProtocolVersion int               `json:"protocol_version"`
	DeviceID        string            `json:"device_id"`
	BatchID         string            `json:"batch_id"`
	AckSequence     uint64            `json:"ack_sequence"`
	Accepted        int               `json:"accepted"`
	Duplicates      int               `json:"duplicates"`
	Rejected        []IngestRejection `json:"rejected"`
	ServerTime      int64             `json:"server_time"`
}

// IngestRejection identifies a permanently invalid envelope or payload record.
// Index is present only for failures tied to an element in a slice payload.
type IngestRejection struct {
	Code    string `json:"code"`
	Index   *int   `json:"index,omitempty"`
	EventID string `json:"event_id,omitempty"`
}

// Heartbeat is latest-only agent state. Server receive time, rather than SentAt,
// determines online status; the client timestamp remains diagnostic metadata.
type Heartbeat struct {
	ProtocolVersion int         `json:"protocol_version"`
	DeviceID        string      `json:"device_id"`
	AgentVersion    string      `json:"agent_version"`
	BootID          string      `json:"boot_id"`
	Sequence        uint64      `json:"sequence"`
	SentAt          int64       `json:"sent_at"`
	Capabilities    []string    `json:"capabilities"`
	QueuedBatches   int         `json:"queued_batches"`
	QueuedBytes     int64       `json:"queued_bytes"`
	OldestQueuedAt  int64       `json:"oldest_queued_at,omitempty"`
	LastScanAt      int64       `json:"last_scan_at,omitempty"`
	LastUploadAt    int64       `json:"last_upload_at,omitempty"`
	Warnings        []string    `json:"warnings,omitempty"`
	ProcessState    *ProcReport `json:"process_state,omitempty"`
}

// ValidateIngestEnvelope returns every permanent validation rejection in
// deterministic envelope-then-event order. A nil result means the envelope is
// structurally safe for authenticated ingest; authentication still binds the
// asserted DeviceID to a server-side principal.
func ValidateIngestEnvelope(envelope IngestEnvelopeV2) []IngestRejection {
	var rejected []IngestRejection
	if envelope.ProtocolVersion != IngestProtocolV2 {
		rejected = append(rejected, IngestRejection{Code: RejectionProtocolVersion})
	}
	deviceValid := validUUID(envelope.DeviceID)
	if !deviceValid {
		rejected = append(rejected, IngestRejection{Code: RejectionDeviceID})
	}
	if !validUUID(envelope.BootID) {
		rejected = append(rejected, IngestRejection{Code: RejectionBootID})
	}
	if !validUUID(envelope.BatchID) {
		rejected = append(rejected, IngestRejection{Code: RejectionBatchID})
	}
	if envelope.CapturedAt <= 0 || envelope.CapturedAt > maxCapturedAtMilli {
		rejected = append(rejected, IngestRejection{Code: RejectionCapturedAt})
	}

	switch envelope.Kind {
	case IngestKindEvents:
		if len(envelope.Events) == 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionMissingEvents})
		}
		if len(envelope.Quotas) > 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedQuotas})
		}
		if envelope.Procs != nil {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedProcs})
		}
		if len(envelope.Events) > MaxIngestBatchEvents {
			rejected = append(rejected, IngestRejection{Code: RejectionBatchTooLarge})
		}

		seen := make(map[string]struct{}, len(envelope.Events))
		for i, event := range envelope.Events {
			if event.EventID == "" {
				rejected = append(rejected, recordRejection(RejectionEventID, i, ""))
			} else {
				if _, duplicate := seen[event.EventID]; duplicate {
					rejected = append(rejected, recordRejection(RejectionDuplicateEventID, i, event.EventID))
				}
				seen[event.EventID] = struct{}{}
			}
			if deviceValid && event.Device != envelope.DeviceID {
				rejected = append(rejected, recordRejection(RejectionEventDeviceMismatch, i, event.EventID))
			}
		}
	case IngestKindQuotas:
		if len(envelope.Quotas) == 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionMissingQuotas})
		}
		if len(envelope.Events) > 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedEvents})
		}
		if envelope.Procs != nil {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedProcs})
		}
		if deviceValid {
			for i, quota := range envelope.Quotas {
				if quota.Device != envelope.DeviceID {
					rejected = append(rejected, recordRejection(RejectionQuotaDeviceMismatch, i, ""))
				}
			}
		}
	case IngestKindProcs:
		if envelope.Procs == nil {
			rejected = append(rejected, IngestRejection{Code: RejectionMissingProcs})
		}
		if len(envelope.Events) > 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedEvents})
		}
		if len(envelope.Quotas) > 0 {
			rejected = append(rejected, IngestRejection{Code: RejectionUnexpectedQuotas})
		}
		if deviceValid && envelope.Procs != nil && envelope.Procs.Device != envelope.DeviceID {
			rejected = append(rejected, IngestRejection{Code: RejectionProcDeviceMismatch})
		}
	default:
		rejected = append(rejected, IngestRejection{Code: RejectionKind})
	}
	return rejected
}

func recordRejection(code string, index int, eventID string) IngestRejection {
	return IngestRejection{Code: code, Index: &index, EventID: eventID}
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if value[i] != '-' {
				return false
			}
		case value[i] >= '0' && value[i] <= '9':
		case value[i] >= 'a' && value[i] <= 'f':
		default:
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}
