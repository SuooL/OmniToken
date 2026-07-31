package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Device identity merge (ADR-0019): the third sanctioned overwrite of stored
// rows, and the only one no code path may trigger by itself.
//
// The other two — `source`'s proxy→tool promotion (ADR-0013) and `device`'s
// observed→self re-attribution (ADR-0015) — run automatically because their
// evidence is inside the system: a row's own `source` value, a batch's channel.
// Nothing in this database can prove that two self-reported names are one
// machine, so there is no guard to write. The safety lives in *who asks*: this
// function is reachable only from an adminAuth'd endpoint the user drives, and
// must never be called from collection, parsing or ingest.
//
// What it may touch is narrow on purpose: attribution columns only. Every count
// in the database is identical before and after — which device a request is
// filed under and how many times it is charged are different questions.

const (
	// DeviceLabelsKey holds hostname → display name (the reversible rename).
	DeviceLabelsKey = "device_labels"
	// DeviceMergesKey holds the append-only audit log of merges. A merge cannot
	// be undone, so this list is the only record of what was changed.
	DeviceMergesKey = "device_merges"
)

var (
	// ErrDeviceMergeSameIdentity: merging a name into itself is a mis-click, and
	// succeeding silently would teach the user the button did something.
	ErrDeviceMergeSameIdentity = errors.New("source and target device are the same identity")
	// ErrDeviceMergeUnknownIdentity guards against a typo reporting a
	// successful merge of nothing at all.
	ErrDeviceMergeUnknownIdentity = errors.New("device identity not found in this database")
)

// DeviceIdentityStats describes one identity as it exists right now, so the
// confirmation dialog can show what is about to be folded into what.
type DeviceIdentityStats struct {
	Device         string `json:"device"`
	Events         int64  `json:"events"`
	TotalTokens    int64  `json:"total_tokens"`
	FirstTS        int64  `json:"first_ts"`
	LastTS         int64  `json:"last_ts"`
	QuotaSnapshots int64  `json:"quota_snapshots"`
	LiveRows       int64  `json:"live_rows"`
}

// DeviceMergePlan is both the preview and the report: PlanDeviceMerge returns
// what would happen, MergeDeviceIdentity returns what did. They are the same
// type because they are computed by the same code — a user must never decide on
// numbers that came from somewhere other than the statements that will run.
type DeviceMergePlan struct {
	From DeviceIdentityStats `json:"from"`
	To   DeviceIdentityStats `json:"to"`
	// EventsMoved is a re-attribution, never a deletion: `events` cannot lose a
	// row to this operation (ADR-0019 §4).
	EventsMoved int64 `json:"events_moved"`
	// QuotaMoved / QuotaDropped: quota_snapshots is the one table whose row
	// count falls, because the same machine under two names can observe the same
	// window at the same millisecond. A snapshot is re-observable state, never a
	// sum, so dropping the duplicate loses nothing countable.
	QuotaMoved   int64 `json:"quota_moved"`
	QuotaDropped int64 `json:"quota_dropped"`
	// LiveRowsDropped counts instantaneous process state that is discarded
	// rather than migrated; the next report rewrites it within seconds.
	LiveRowsDropped int64 `json:"live_rows_dropped"`
	LabelMoved      bool  `json:"label_moved"`
}

// DeviceMergeRecord is one entry of the audit log. The row counts are part of
// it because after an irreversible change they are the only thing left to check
// "how much did this actually touch" against.
type DeviceMergeRecord struct {
	From            string `json:"from"`
	To              string `json:"to"`
	At              int64  `json:"at"`
	Actor           string `json:"actor"`
	Events          int64  `json:"events"`
	QuotaSnapshots  int64  `json:"quota_snapshots"`
	QuotaDropped    int64  `json:"quota_dropped"`
	LiveRowsDropped int64  `json:"live_rows_dropped"`
}

// rowQueryer lets the planning queries run identically on the database handle
// (preview) and inside the merge transaction (execution).
type rowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// PlanDeviceMerge computes what merging `from` into `to` would do, without
// changing anything. It applies the same validation as the merge, so a typo is
// rejected at preview time rather than after the confirmation dialog.
func (s *Store) PlanDeviceMerge(from, to string) (DeviceMergePlan, error) {
	if err := s.ensureSettings(); err != nil {
		return DeviceMergePlan{}, err
	}
	return planDeviceMerge(s.db, from, to)
}

func planDeviceMerge(q rowQueryer, from, to string) (DeviceMergePlan, error) {
	var plan DeviceMergePlan
	if from == "" || to == "" {
		return plan, fmt.Errorf("%w: %q", ErrDeviceMergeUnknownIdentity, "")
	}
	if from == to {
		return plan, ErrDeviceMergeSameIdentity
	}
	for _, device := range []string{from, to} {
		exists, err := deviceIdentityExists(q, device)
		if err != nil {
			return plan, err
		}
		if !exists {
			return plan, fmt.Errorf("%w: %q", ErrDeviceMergeUnknownIdentity, device)
		}
	}
	var err error
	if plan.From, err = deviceIdentityStats(q, from); err != nil {
		return plan, err
	}
	if plan.To, err = deviceIdentityStats(q, to); err != nil {
		return plan, err
	}
	plan.EventsMoved = plan.From.Events
	if err := q.QueryRow(quotaCollisionCount, from, to).Scan(&plan.QuotaDropped); err != nil {
		return plan, err
	}
	plan.QuotaMoved = plan.From.QuotaSnapshots - plan.QuotaDropped
	plan.LiveRowsDropped = plan.From.LiveRows

	labels, err := deviceLabels(q)
	if err != nil {
		return plan, err
	}
	_, hasFrom := labels[from]
	_, hasTo := labels[to]
	plan.LabelMoved = hasFrom && !hasTo
	return plan, nil
}

// quotaCollisionCount counts source rows whose whole primary key already exists
// under the target — the rows the merge drops instead of re-attributing.
const quotaCollisionCount = `SELECT COUNT(*) FROM quota_snapshots s WHERE s.device = ? AND EXISTS (
	SELECT 1 FROM quota_snapshots t WHERE t.device = ?
	  AND t.source = s.source AND t.limit_id = s.limit_id AND t.scope = s.scope
	  AND t.window_minutes = s.window_minutes AND t.observed_at = s.observed_at)`

// deviceIdentityExists asks whether this string is an identity the database has
// ever filed anything under. The device registry is deliberately not consulted:
// it holds credentials, takes no part in a merge, and a registered-but-silent
// device has nothing to merge.
func deviceIdentityExists(q rowQueryer, device string) (bool, error) {
	var exists int
	err := q.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM events WHERE device = ?1)
		     OR EXISTS(SELECT 1 FROM quota_snapshots WHERE device = ?1)
		     OR EXISTS(SELECT 1 FROM live_sessions WHERE device = ?1)
		     OR EXISTS(SELECT 1 FROM live_reports WHERE device = ?1)`, device).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

func deviceIdentityStats(q rowQueryer, device string) (DeviceIdentityStats, error) {
	stats := DeviceIdentityStats{Device: device}
	err := q.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0),
		        COALESCE(MIN(ts),0), COALESCE(MAX(ts),0)
		 FROM events WHERE device = ?`, device).
		Scan(&stats.Events, &stats.TotalTokens, &stats.FirstTS, &stats.LastTS)
	if err != nil {
		return stats, err
	}
	if err := q.QueryRow(`SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, device).
		Scan(&stats.QuotaSnapshots); err != nil {
		return stats, err
	}
	var sessions, reports int64
	if err := q.QueryRow(`SELECT COUNT(*) FROM live_sessions WHERE device = ?`, device).Scan(&sessions); err != nil {
		return stats, err
	}
	if err := q.QueryRow(`SELECT COUNT(*) FROM live_reports WHERE device = ?`, device).Scan(&reports); err != nil {
		return stats, err
	}
	stats.LiveRows = sessions + reports
	return stats, nil
}

// MergeDeviceIdentity files everything recorded under `from` as `to`, in one
// transaction, and appends an audit record. It returns what it did.
//
// Any failure rolls the whole thing back: a half-merged identity would be worse
// than either end state, because the user cannot tell which half moved.
func (s *Store) MergeDeviceIdentity(from, to, actor string, at int64) (DeviceMergePlan, error) {
	if err := s.ensureSettings(); err != nil {
		return DeviceMergePlan{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DeviceMergePlan{}, err
	}
	defer tx.Rollback()

	// Planned inside the transaction, so the numbers reported (and audited) are
	// the ones this statement sequence is about to act on.
	plan, err := planDeviceMerge(tx, from, to)
	if err != nil {
		return DeviceMergePlan{}, err
	}

	// Order matters and cannot be swapped. `UPDATE OR REPLACE` looks like the
	// one-statement version of this and is exactly backwards: SQLite resolves
	// the conflict by deleting the row that already exists — the target's — and
	// keeping the row being updated. Deleting the source's duplicates first, then
	// moving what is left, keeps the target's observation as decided.
	dropped, err := affected(tx.Exec(
		`DELETE FROM quota_snapshots WHERE device = ?1 AND EXISTS (
		   SELECT 1 FROM quota_snapshots t WHERE t.device = ?2
		     AND t.source = quota_snapshots.source AND t.limit_id = quota_snapshots.limit_id
		     AND t.scope = quota_snapshots.scope
		     AND t.window_minutes = quota_snapshots.window_minutes
		     AND t.observed_at = quota_snapshots.observed_at)`, from, to))
	if err != nil {
		return DeviceMergePlan{}, err
	}
	movedQuota, err := affected(tx.Exec(`UPDATE quota_snapshots SET device = ?2 WHERE device = ?1`, from, to))
	if err != nil {
		return DeviceMergePlan{}, err
	}

	// Process state is a snapshot the reporting device rewrites every round
	// (ADR-0012), and its primary key contains the device. Migrating it would
	// either collide on a shared PID or briefly show a session that is not there.
	droppedSessions, err := affected(tx.Exec(`DELETE FROM live_sessions WHERE device = ?`, from))
	if err != nil {
		return DeviceMergePlan{}, err
	}
	droppedReports, err := affected(tx.Exec(`DELETE FROM live_reports WHERE device = ?`, from))
	if err != nil {
		return DeviceMergePlan{}, err
	}

	// The whole point, and the narrowest statement in the file: one column.
	// `event_id` never contains the device (ADR-0004), so every id exists on
	// exactly one row and this can neither collide nor collapse two events into
	// one. No count column appears here, and none ever may.
	//
	// `device_origin` is left alone deliberately: it records how this row's
	// device value was obtained, and the merge did not change that. A row that
	// came in as `observed` stays correctable by a later self-report, which is
	// stronger evidence than a manual merge.
	movedEvents, err := affected(tx.Exec(`UPDATE events SET device = ?2 WHERE device = ?1`, from, to))
	if err != nil {
		return DeviceMergePlan{}, err
	}

	// A disagreement between plan and execution means the two disagree about
	// what the database contains; refusing to commit is the only safe response.
	if movedEvents != plan.EventsMoved || movedQuota != plan.QuotaMoved || dropped != plan.QuotaDropped ||
		droppedSessions+droppedReports != plan.LiveRowsDropped {
		return DeviceMergePlan{}, fmt.Errorf(
			"device merge aborted: preview said %d/%d/%d/%d rows but the statements touched %d/%d/%d/%d",
			plan.EventsMoved, plan.QuotaMoved, plan.QuotaDropped, plan.LiveRowsDropped,
			movedEvents, movedQuota, dropped, droppedSessions+droppedReports)
	}

	if err := mergeDeviceLabel(tx, from, to); err != nil {
		return DeviceMergePlan{}, err
	}
	record := DeviceMergeRecord{
		From: from, To: to, At: at, Actor: actor,
		Events: plan.EventsMoved, QuotaSnapshots: plan.QuotaMoved,
		QuotaDropped: plan.QuotaDropped, LiveRowsDropped: plan.LiveRowsDropped,
	}
	if err := appendDeviceMergeRecord(tx, record); err != nil {
		return DeviceMergePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeviceMergePlan{}, err
	}
	return plan, nil
}

// mergeDeviceLabel carries the source's display name over when the target has
// none. Renaming is a display-only setting, but leaving the old key behind
// would resurrect a name the user just merged away.
func mergeDeviceLabel(tx *sql.Tx, from, to string) error {
	labels, err := deviceLabels(tx)
	if err != nil {
		return err
	}
	label, ok := labels[from]
	if !ok {
		return nil
	}
	delete(labels, from)
	if _, taken := labels[to]; !taken {
		labels[to] = label
	}
	return setSettingsJSONTx(tx, DeviceLabelsKey, labels)
}

// appendDeviceMergeRecord adds one entry to the audit log. A document that does
// not decode is a hard error: writing over it would destroy the record of every
// earlier merge, which is precisely what this list exists to prevent.
func appendDeviceMergeRecord(tx *sql.Tx, record DeviceMergeRecord) error {
	history, err := deviceMergeHistory(tx)
	if err != nil {
		return err
	}
	return setSettingsJSONTx(tx, DeviceMergesKey, append(history, record))
}

// DeviceMergeHistory returns the audit log, oldest first. There is no delete
// counterpart on purpose (ADR-0019 §5).
func (s *Store) DeviceMergeHistory() ([]DeviceMergeRecord, error) {
	if err := s.ensureSettings(); err != nil {
		return nil, err
	}
	return deviceMergeHistory(s.db)
}

func deviceMergeHistory(q rowQueryer) ([]DeviceMergeRecord, error) {
	raw, err := settingValue(q, DeviceMergesKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var history []DeviceMergeRecord
	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		return nil, fmt.Errorf("device merge history is unreadable (%s): %w", DeviceMergesKey, err)
	}
	return history, nil
}

func deviceLabels(q rowQueryer) (map[string]string, error) {
	labels := map[string]string{}
	raw, err := settingValue(q, DeviceLabelsKey)
	if err != nil || raw == "" {
		return labels, err
	}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, fmt.Errorf("device labels are unreadable (%s): %w", DeviceLabelsKey, err)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return labels, nil
}

func settingValue(q rowQueryer, key string) (string, error) {
	var value string
	err := q.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func setSettingsJSONTx(tx *sql.Tx, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO app_settings (key, value) VALUES (?, ?)
	 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(data))
	return err
}

// SelfReportedDevices lists the device names that have ever attributed an event
// to themselves (ADR-0015 `self`). It backs the startup hint of ADR-0019 §7.3,
// whose trigger is a fact rather than a similarity guess: this machine's
// resolved name and its hostname have both self-reported, so they are two
// identities for one machine. Observed rows are excluded — a mirror's guess
// about some other host says nothing about who we are.
func (s *Store) SelfReportedDevices() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT device FROM events
		 WHERE device_origin = 'self' AND device != '' ORDER BY device`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var device string
		if err := rows.Scan(&device); err != nil {
			return nil, err
		}
		out = append(out, device)
	}
	return out, rows.Err()
}

func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
