package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/suool/omnitoken/internal/model"
)

const ingestReceiptSchema = `
CREATE TABLE IF NOT EXISTS ingest_receipts (
	batch_id TEXT PRIMARY KEY,
	device_id TEXT NOT NULL,
	protocol_version INTEGER NOT NULL,
	ack_sequence TEXT NOT NULL,
	accepted INTEGER NOT NULL,
	duplicates INTEGER NOT NULL,
	server_time INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ingest_receipts_device
	ON ingest_receipts(device_id, server_time);
`

var ErrIngestReceiptConflict = errors.New("ingest receipt belongs to another device")

// IngestV2Result separates durable acknowledgement from UI-visible mutation.
// Replays return the original Ack with Replay=true and never mutate payload
// tables.
type IngestV2Result struct {
	Ack     model.IngestAckV2
	Replay  bool
	Mutated bool
}

// ApplyIngestV2 applies exactly one already-validated envelope payload and
// persists its acknowledgement receipt in the same database transaction.
func (s *Store) ApplyIngestV2(envelope model.IngestEnvelopeV2, receivedAt int64) (IngestV2Result, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return IngestV2Result{}, err
	}
	defer tx.Rollback()

	receipt, found, err := ingestReceiptTx(tx, envelope.BatchID)
	if err != nil {
		return IngestV2Result{}, err
	}
	if found {
		if receipt.DeviceID != envelope.DeviceID {
			return IngestV2Result{}, ErrIngestReceiptConflict
		}
		return IngestV2Result{Ack: receipt, Replay: true}, nil
	}

	ack := model.IngestAckV2{
		ProtocolVersion: envelope.ProtocolVersion,
		DeviceID:        envelope.DeviceID,
		BatchID:         envelope.BatchID,
		AckSequence:     envelope.Sequence,
		Rejected:        []model.IngestRejection{},
		ServerTime:      receivedAt,
	}
	result := IngestV2Result{Ack: ack}
	var eventResult eventApplyResult

	switch envelope.Kind {
	case model.IngestKindEvents:
		eventResult, err = insertEventsFromTx(tx, envelope.Events, receivedAt, OriginSelf)
		result.Ack.Accepted = eventResult.inserted
		result.Ack.Duplicates = len(envelope.Events) - eventResult.inserted
		result.Mutated = eventResult.mutated()
	case model.IngestKindQuotas:
		result.Ack.Accepted, err = insertQuotasTx(tx, envelope.Quotas)
		result.Ack.Duplicates = len(envelope.Quotas) - result.Ack.Accepted
		result.Mutated = result.Ack.Accepted > 0
	case model.IngestKindProcs:
		if envelope.Procs == nil {
			err = errors.New("missing process payload")
			break
		}
		result.Mutated, err = applyProcReportTx(tx, *envelope.Procs)
		result.Ack.Accepted = 1
	default:
		err = fmt.Errorf("unsupported ingest kind %q", envelope.Kind)
	}
	if err != nil {
		return IngestV2Result{}, err
	}

	if _, err := tx.Exec(`
		INSERT INTO ingest_receipts
			(batch_id, device_id, protocol_version, ack_sequence,
			 accepted, duplicates, server_time)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		result.Ack.BatchID,
		result.Ack.DeviceID,
		result.Ack.ProtocolVersion,
		strconv.FormatUint(result.Ack.AckSequence, 10),
		result.Ack.Accepted,
		result.Ack.Duplicates,
		result.Ack.ServerTime,
	); err != nil {
		return IngestV2Result{}, fmt.Errorf("persist ingest receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IngestV2Result{}, err
	}
	logEventApply(eventResult)
	return result, nil
}

func ingestReceiptTx(tx *sql.Tx, batchID string) (model.IngestAckV2, bool, error) {
	var ack model.IngestAckV2
	var sequence string
	err := tx.QueryRow(`
		SELECT protocol_version, device_id, batch_id, ack_sequence,
		       accepted, duplicates, server_time
		FROM ingest_receipts
		WHERE batch_id = ?`,
		batchID,
	).Scan(
		&ack.ProtocolVersion,
		&ack.DeviceID,
		&ack.BatchID,
		&sequence,
		&ack.Accepted,
		&ack.Duplicates,
		&ack.ServerTime,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.IngestAckV2{}, false, nil
	}
	if err != nil {
		return model.IngestAckV2{}, false, err
	}
	ack.AckSequence, err = strconv.ParseUint(sequence, 10, 64)
	if err != nil {
		return model.IngestAckV2{}, false, fmt.Errorf("decode ingest receipt sequence: %w", err)
	}
	ack.Rejected = []model.IngestRejection{}
	return ack, true, nil
}

func insertQuotasTx(tx *sql.Tx, quotas []model.QuotaSnapshot) (int, error) {
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO quota_snapshots
		(device, source, limit_id, scope, window_minutes, used_percent, resets_at, observed_at, plan_type)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, quota := range quotas {
		result, err := stmt.Exec(
			quota.Device,
			quota.Source,
			quota.LimitID,
			quota.Scope,
			quota.WindowMinutes,
			quota.UsedPercent,
			quota.ResetsAt,
			quota.ObservedAt,
			quota.PlanType,
		)
		if err != nil {
			return inserted, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func applyProcReportTx(tx *sql.Tx, report model.ProcReport) (bool, error) {
	if report.Device == "" || report.ObservedAt == 0 {
		return false, nil
	}
	var previous int64
	err := tx.QueryRow(`SELECT observed_at FROM live_reports WHERE device = ?`, report.Device).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if previous > report.ObservedAt {
		return false, nil
	}

	known := map[int]bool{}
	rows, err := tx.Query(`SELECT pid FROM live_sessions WHERE device = ?`, report.Device)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return false, err
		}
		known[pid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	changed := len(known) != len(report.Sessions)

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO live_sessions
		(device, pid, source, started_at, observed_at) VALUES (?,?,?,?,?)`)
	if err != nil {
		return false, err
	}
	defer stmt.Close()
	for _, session := range report.Sessions {
		if !known[session.PID] {
			changed = true
		}
		if _, err := stmt.Exec(
			report.Device,
			session.PID,
			session.Source,
			session.StartedAt,
			report.ObservedAt,
		); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(
		`DELETE FROM live_sessions WHERE device = ? AND observed_at < ?`,
		report.Device,
		report.ObservedAt,
	); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO live_reports (device, observed_at) VALUES (?, ?)`,
		report.Device,
		report.ObservedAt,
	); err != nil {
		return false, err
	}
	return changed, nil
}
