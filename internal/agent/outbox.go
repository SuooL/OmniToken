package agent

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/suool/omnitoken/internal/model"
	_ "modernc.org/sqlite"
)

const (
	outboxSchemaVersion   = 2
	DefaultOutboxMaxBytes = 64 << 20
)

// RawBatch is the wire-ready oldest outbox entry alongside its envelope
// identity columns so uploading and acknowledging skip redundant 16 MiB JSON
// unmarshal/marshal round-trips.
type RawBatch struct {
	ID              int64
	BatchID         string
	DeviceID        string
	ProtocolVersion int
	Sequence        uint64
	Payload         []byte
}

var (
	ErrOutboxEmpty            = errors.New("outbox is empty")
	ErrOutboxFull             = errors.New("outbox capacity exceeded")
	ErrOutboxConflict         = errors.New("batch ID already contains different payload")
	ErrEnvelopeTooLarge       = errors.New("ingest envelope exceeds protocol byte limit")
	ErrInvalidAcknowledgement = errors.New("acknowledgement does not match oldest batch")
)

type OutboxStats struct {
	QueuedBatches  int
	QueuedBytes    int64
	OldestQueuedAt int64
	MaxBytes       int64
}

// Outbox persists v2 ingest envelopes until the server acknowledges their
// exact delivery identity.
type Outbox struct {
	db       *sql.DB
	maxBytes int64
}

func OpenOutbox(path string, maxBytes int64) (*Outbox, error) {
	if path == "" {
		return nil, errors.New("outbox path is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultOutboxMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create outbox directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open outbox: %w", err)
	}
	db.SetMaxOpenConns(1)
	outbox := &Outbox{db: db, maxBytes: maxBytes}
	if err := outbox.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return outbox, nil
}

func (o *Outbox) initialize() error {
	var version int
	if err := o.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read outbox schema version: %w", err)
	}
	if version == 0 {
		// auto_vacuum must be configured before creating the first table.
		_, _ = o.db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`)
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := o.db.Exec(pragma); err != nil {
			return fmt.Errorf("configure outbox: %w", err)
		}
	}

	if version != 0 && version != 1 && version != outboxSchemaVersion {
		return fmt.Errorf("unsupported outbox schema version %d", version)
	}
	if version == outboxSchemaVersion {
		return nil
	}

	tx, err := o.beginImmediate()
	if err != nil {
		return fmt.Errorf("begin outbox migration: %w", err)
	}
	defer tx.Rollback()

	if version == 0 {
		if _, err := tx.Exec(`
			CREATE TABLE outbox_batches (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				batch_id TEXT NOT NULL UNIQUE,
				device_id TEXT NOT NULL DEFAULT '',
				protocol_version INTEGER NOT NULL DEFAULT 0,
				sequence TEXT NOT NULL DEFAULT '0',
				envelope BLOB NOT NULL,
				payload_bytes INTEGER NOT NULL,
				enqueued_at INTEGER NOT NULL
			);
			CREATE TABLE outbox_meta (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL
			);
			CREATE TABLE outbox_dead_letters (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				batch_id TEXT NOT NULL,
				envelope BLOB NOT NULL,
				payload_bytes INTEGER NOT NULL,
				enqueued_at INTEGER NOT NULL,
				failed_at INTEGER NOT NULL,
				http_status INTEGER NOT NULL,
				reason TEXT NOT NULL DEFAULT ''
			);
			INSERT INTO outbox_meta(key, value) VALUES ('last_sequence', '0');
		`); err != nil {
			return fmt.Errorf("create outbox schema: %w", err)
		}
	} else if version == 1 {
		for _, stmt := range []string{
			`ALTER TABLE outbox_batches ADD COLUMN device_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE outbox_batches ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE outbox_batches ADD COLUMN sequence TEXT NOT NULL DEFAULT '0'`,
			`CREATE TABLE IF NOT EXISTS outbox_dead_letters (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				batch_id TEXT NOT NULL,
				envelope BLOB NOT NULL,
				payload_bytes INTEGER NOT NULL,
				enqueued_at INTEGER NOT NULL,
				failed_at INTEGER NOT NULL,
				http_status INTEGER NOT NULL,
				reason TEXT NOT NULL DEFAULT ''
			)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("upgrade outbox v1->v2: %w", err)
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, outboxSchemaVersion)); err != nil {
		return fmt.Errorf("set outbox schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox migration: %w", err)
	}
	return nil
}

func (o *Outbox) beginImmediate() (*sql.Tx, error) {
	// With MaxOpenConns(1) a standard transaction acquires the write lock on
	// first write, and using an explicit IMMEDIATE lock avoids busy upgrades
	// if another local process inspects the SQLite file concurrently.
	tx, err := o.db.Begin()
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (o *Outbox) Close() error {
	return o.db.Close()
}

func (o *Outbox) Enqueue(envelope model.IngestEnvelopeV2) error {
	payload, err := encodeOutboxEnvelope(envelope)
	if err != nil {
		return err
	}

	tx, err := o.beginImmediate()
	if err != nil {
		return fmt.Errorf("begin enqueue: %w", err)
	}
	defer tx.Rollback()
	if err := o.enqueuePayloadTx(tx, envelope, payload, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enqueue: %w", err)
	}
	return nil
}

// EnqueueNext allocates a sequence and inserts the row in one transaction.
// This is the production enqueue primitive: FIFO insertion order and protocol
// sequence order cannot diverge even when scanner, heartbeat-adjacent, and
// proxy producers run concurrently.
func (o *Outbox) EnqueueNext(envelope model.IngestEnvelopeV2) (model.IngestEnvelopeV2, error) {
	envelopes, err := o.EnqueueManyNext([]model.IngestEnvelopeV2{envelope})
	if err != nil {
		return model.IngestEnvelopeV2{}, err
	}
	return envelopes[0], nil
}

// EnqueueManyNext atomically persists a logical delivery that had to be split
// into multiple protocol-sized envelopes. Either every chunk is durable with
// consecutive FIFO sequences or none is, so a source cursor can safely treat
// the call as one sink outcome.
func (o *Outbox) EnqueueManyNext(envelopes []model.IngestEnvelopeV2) ([]model.IngestEnvelopeV2, error) {
	if len(envelopes) == 0 {
		return []model.IngestEnvelopeV2{}, nil
	}

	tx, err := o.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin sequence enqueue: %w", err)
	}
	defer tx.Rollback()

	var encodedSequence string
	if err := tx.QueryRow(`SELECT value FROM outbox_meta WHERE key = 'last_sequence'`).Scan(&encodedSequence); err != nil {
		return nil, fmt.Errorf("read outbox sequence: %w", err)
	}
	current, err := strconv.ParseUint(encodedSequence, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse outbox sequence: %w", err)
	}

	assigned := make([]model.IngestEnvelopeV2, len(envelopes))
	payloads := make([][]byte, len(envelopes))
	var addedBytes int64
	for i, envelope := range envelopes {
		if current == math.MaxUint64 {
			return nil, errors.New("outbox sequence exhausted")
		}
		current++
		envelope.Sequence = current
		payload, err := encodeOutboxEnvelope(envelope)
		if err != nil {
			return nil, err
		}
		assigned[i] = envelope
		payloads[i] = payload
		addedBytes += int64(len(payload))
	}

	var queuedBytes int64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(payload_bytes), 0) FROM outbox_batches`).Scan(&queuedBytes); err != nil {
		return nil, fmt.Errorf("read outbox capacity: %w", err)
	}
	if addedBytes > o.maxBytes-queuedBytes {
		return nil, ErrOutboxFull
	}
	enqueuedAt := time.Now().UnixMilli()
	for i, envelope := range assigned {
		if err := o.insertNewPayloadTx(tx, envelope, payloads[i], enqueuedAt); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(
		`UPDATE outbox_meta SET value = ? WHERE key = 'last_sequence'`,
		strconv.FormatUint(current, 10),
	); err != nil {
		return nil, fmt.Errorf("persist outbox sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit sequence enqueue: %w", err)
	}
	return assigned, nil
}

func encodeOutboxEnvelope(envelope model.IngestEnvelopeV2) ([]byte, error) {
	if rejected := model.ValidateIngestEnvelope(envelope); len(rejected) != 0 {
		return nil, fmt.Errorf("invalid ingest envelope: %s", rejected[0].Code)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode ingest envelope: %w", err)
	}
	if len(payload) > model.MaxIngestEnvelopeBytes {
		return nil, ErrEnvelopeTooLarge
	}
	return payload, nil
}

func (o *Outbox) enqueuePayloadTx(tx *sql.Tx, envelope model.IngestEnvelopeV2, payload []byte, enqueuedAt int64) error {
	var existing []byte
	err := tx.QueryRow(`SELECT envelope FROM outbox_batches WHERE batch_id = ?`, envelope.BatchID).Scan(&existing)
	switch {
	case err == nil && bytes.Equal(existing, payload):
		return nil
	case err == nil:
		return ErrOutboxConflict
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("check duplicate batch: %w", err)
	}
	var queuedBytes int64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(payload_bytes), 0) FROM outbox_batches`).Scan(&queuedBytes); err != nil {
		return fmt.Errorf("read outbox capacity: %w", err)
	}
	if int64(len(payload)) > o.maxBytes-queuedBytes {
		return ErrOutboxFull
	}
	return o.insertNewPayloadTx(tx, envelope, payload, enqueuedAt)
}

func (o *Outbox) insertNewPayloadTx(tx *sql.Tx, envelope model.IngestEnvelopeV2, payload []byte, enqueuedAt int64) error {
	if _, err := tx.Exec(
		`INSERT INTO outbox_batches(batch_id, device_id, protocol_version, sequence, envelope, payload_bytes, enqueued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		envelope.BatchID,
		envelope.DeviceID,
		envelope.ProtocolVersion,
		strconv.FormatUint(envelope.Sequence, 10),
		payload,
		len(payload),
		enqueuedAt,
	); err != nil {
		return fmt.Errorf("enqueue batch: %w", err)
	}
	return nil
}

// PeekRaw returns the wire-ready JSON bytes and identity columns for the
// oldest batch without unmarshaling the full payload.
func (o *Outbox) PeekRaw() (RawBatch, error) {
	var raw RawBatch
	var seqStr string
	if err := o.db.QueryRow(
		`SELECT id, batch_id, device_id, protocol_version, sequence, envelope
		 FROM outbox_batches ORDER BY id LIMIT 1`,
	).Scan(&raw.ID, &raw.BatchID, &raw.DeviceID, &raw.ProtocolVersion, &seqStr, &raw.Payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RawBatch{}, ErrOutboxEmpty
		}
		return RawBatch{}, fmt.Errorf("read oldest batch: %w", err)
	}
	if raw.ProtocolVersion == 0 || raw.DeviceID == "" {
		// Legacy v1 row upgraded before headers were stored in columns.
		var envelope model.IngestEnvelopeV2
		if err := json.Unmarshal(raw.Payload, &envelope); err != nil {
			return RawBatch{}, fmt.Errorf("decode oldest batch: %w", err)
		}
		raw.DeviceID = envelope.DeviceID
		raw.ProtocolVersion = envelope.ProtocolVersion
		raw.Sequence = envelope.Sequence
		return raw, nil
	}
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return RawBatch{}, fmt.Errorf("parse oldest batch sequence: %w", err)
	}
	raw.Sequence = seq
	return raw, nil
}

func (o *Outbox) PeekBatch() (model.IngestEnvelopeV2, error) {
	raw, err := o.PeekRaw()
	if err != nil {
		return model.IngestEnvelopeV2{}, err
	}
	var envelope model.IngestEnvelopeV2
	if err := json.Unmarshal(raw.Payload, &envelope); err != nil {
		return model.IngestEnvelopeV2{}, fmt.Errorf("decode oldest batch: %w", err)
	}
	return envelope, nil
}

func (o *Outbox) Acknowledge(ack model.IngestAckV2) error {
	tx, err := o.beginImmediate()
	if err != nil {
		return fmt.Errorf("begin acknowledgement: %w", err)
	}
	defer tx.Rollback()

	var id int64
	var batchID, deviceID, seqStr string
	var protoVer int
	var payload []byte
	if err := tx.QueryRow(
		`SELECT id, batch_id, device_id, protocol_version, sequence, envelope
		 FROM outbox_batches ORDER BY id LIMIT 1`,
	).Scan(&id, &batchID, &deviceID, &protoVer, &seqStr, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOutboxEmpty
		}
		return fmt.Errorf("read acknowledged batch: %w", err)
	}
	var seq uint64
	if protoVer == 0 || deviceID == "" {
		var envelope model.IngestEnvelopeV2
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("decode acknowledged batch: %w", err)
		}
		protoVer = envelope.ProtocolVersion
		deviceID = envelope.DeviceID
		batchID = envelope.BatchID
		seq = envelope.Sequence
	} else {
		parsed, err := strconv.ParseUint(seqStr, 10, 64)
		if err != nil {
			return fmt.Errorf("parse acknowledged batch sequence: %w", err)
		}
		seq = parsed
	}
	if ack.ProtocolVersion != protoVer ||
		ack.DeviceID != deviceID ||
		ack.BatchID != batchID ||
		ack.AckSequence != seq {
		return ErrInvalidAcknowledgement
	}
	if _, err := tx.Exec(`DELETE FROM outbox_batches WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete acknowledged batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit acknowledgement: %w", err)
	}
	return nil
}

// QuarantineOldest moves a permanently rejected oldest batch (HTTP 400/409/413/422)
// into outbox_dead_letters so it cannot wedge FIFO delivery of subsequent healthy
// batches while preserving the raw payload for post-mortem inspection.
func (o *Outbox) QuarantineOldest(batchID string, httpStatus int, reason string) error {
	tx, err := o.beginImmediate()
	if err != nil {
		return fmt.Errorf("begin dead-letter quarantine: %w", err)
	}
	defer tx.Rollback()

	var id, payloadBytes, enqueuedAt int64
	var gotBatchID string
	var payload []byte
	if err := tx.QueryRow(
		`SELECT id, batch_id, envelope, payload_bytes, enqueued_at
		 FROM outbox_batches ORDER BY id LIMIT 1`,
	).Scan(&id, &gotBatchID, &payload, &payloadBytes, &enqueuedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOutboxEmpty
		}
		return fmt.Errorf("read oldest batch for quarantine: %w", err)
	}
	if gotBatchID != batchID {
		return ErrInvalidAcknowledgement
	}
	if _, err := tx.Exec(
		`INSERT INTO outbox_dead_letters
		   (batch_id, envelope, payload_bytes, enqueued_at, failed_at, http_status, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		gotBatchID, payload, payloadBytes, enqueuedAt, time.Now().UnixMilli(), httpStatus, reason,
	); err != nil {
		return fmt.Errorf("insert dead letter: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM outbox_batches WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove quarantined batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit quarantine: %w", err)
	}
	_, _ = o.db.Exec(`PRAGMA incremental_vacuum(64)`)
	return nil
}

// DeadLetterCount returns how many permanently rejected batches have been quarantined.
func (o *Outbox) DeadLetterCount() (int, error) {
	var n int
	if err := o.db.QueryRow(`SELECT COUNT(*) FROM outbox_dead_letters`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (o *Outbox) Stats() (OutboxStats, error) {
	stats := OutboxStats{MaxBytes: o.maxBytes}
	if err := o.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(payload_bytes), 0), COALESCE(MIN(enqueued_at), 0)
		FROM outbox_batches
	`).Scan(&stats.QueuedBatches, &stats.QueuedBytes, &stats.OldestQueuedAt); err != nil {
		return OutboxStats{}, fmt.Errorf("read outbox stats: %w", err)
	}
	return stats, nil
}
