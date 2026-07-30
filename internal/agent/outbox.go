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
	outboxSchemaVersion   = 1
	DefaultOutboxMaxBytes = 64 << 20
)

var (
	ErrOutboxEmpty            = errors.New("outbox is empty")
	ErrOutboxFull             = errors.New("outbox capacity exceeded")
	ErrOutboxConflict         = errors.New("batch ID already contains different payload")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
	return outbox, nil
}

func (o *Outbox) initialize() error {
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := o.db.Exec(pragma); err != nil {
			return fmt.Errorf("configure outbox: %w", err)
		}
	}

	var version int
	if err := o.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read outbox schema version: %w", err)
	}
	if version != 0 && version != outboxSchemaVersion {
		return fmt.Errorf("unsupported outbox schema version %d", version)
	}
	if version == outboxSchemaVersion {
		return nil
	}

	tx, err := o.db.Begin()
	if err != nil {
		return fmt.Errorf("begin outbox migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE outbox_batches (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id TEXT NOT NULL UNIQUE,
			envelope BLOB NOT NULL,
			payload_bytes INTEGER NOT NULL,
			enqueued_at INTEGER NOT NULL
		);
		CREATE TABLE outbox_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO outbox_meta(key, value) VALUES ('last_sequence', '0');
	`); err != nil {
		return fmt.Errorf("create outbox schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, outboxSchemaVersion)); err != nil {
		return fmt.Errorf("set outbox schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox migration: %w", err)
	}
	return nil
}

func (o *Outbox) Close() error {
	return o.db.Close()
}

func (o *Outbox) Enqueue(envelope model.IngestEnvelopeV2) error {
	if rejected := model.ValidateIngestEnvelope(envelope); len(rejected) != 0 {
		return fmt.Errorf("invalid ingest envelope: %s", rejected[0].Code)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode ingest envelope: %w", err)
	}

	tx, err := o.db.Begin()
	if err != nil {
		return fmt.Errorf("begin enqueue: %w", err)
	}
	defer tx.Rollback()

	var existing []byte
	err = tx.QueryRow(`SELECT envelope FROM outbox_batches WHERE batch_id = ?`, envelope.BatchID).Scan(&existing)
	switch {
	case err == nil && bytes.Equal(existing, payload):
		return tx.Commit()
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
	if _, err := tx.Exec(
		`INSERT INTO outbox_batches(batch_id, envelope, payload_bytes, enqueued_at) VALUES (?, ?, ?, ?)`,
		envelope.BatchID, payload, len(payload), time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("enqueue batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enqueue: %w", err)
	}
	return nil
}

func (o *Outbox) PeekBatch() (model.IngestEnvelopeV2, error) {
	var payload []byte
	if err := o.db.QueryRow(`SELECT envelope FROM outbox_batches ORDER BY id LIMIT 1`).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.IngestEnvelopeV2{}, ErrOutboxEmpty
		}
		return model.IngestEnvelopeV2{}, fmt.Errorf("read oldest batch: %w", err)
	}
	var envelope model.IngestEnvelopeV2
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return model.IngestEnvelopeV2{}, fmt.Errorf("decode oldest batch: %w", err)
	}
	return envelope, nil
}

func (o *Outbox) Acknowledge(ack model.IngestAckV2) error {
	tx, err := o.db.Begin()
	if err != nil {
		return fmt.Errorf("begin acknowledgement: %w", err)
	}
	defer tx.Rollback()

	var id int64
	var payload []byte
	if err := tx.QueryRow(`SELECT id, envelope FROM outbox_batches ORDER BY id LIMIT 1`).Scan(&id, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOutboxEmpty
		}
		return fmt.Errorf("read acknowledged batch: %w", err)
	}
	var envelope model.IngestEnvelopeV2
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode acknowledged batch: %w", err)
	}
	if ack.ProtocolVersion != envelope.ProtocolVersion ||
		ack.DeviceID != envelope.DeviceID ||
		ack.BatchID != envelope.BatchID ||
		ack.AckSequence != envelope.Sequence {
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

func (o *Outbox) NextSequence() (uint64, error) {
	tx, err := o.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin sequence allocation: %w", err)
	}
	defer tx.Rollback()

	var encoded string
	if err := tx.QueryRow(`SELECT value FROM outbox_meta WHERE key = 'last_sequence'`).Scan(&encoded); err != nil {
		return 0, fmt.Errorf("read outbox sequence: %w", err)
	}
	current, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse outbox sequence: %w", err)
	}
	if current == math.MaxUint64 {
		return 0, errors.New("outbox sequence exhausted")
	}
	next := current + 1
	if _, err := tx.Exec(`UPDATE outbox_meta SET value = ? WHERE key = 'last_sequence'`, strconv.FormatUint(next, 10)); err != nil {
		return 0, fmt.Errorf("persist outbox sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit outbox sequence: %w", err)
	}
	return next, nil
}
