package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/suool/omnitoken/internal/model"
)

const (
	outboxDeviceID = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	outboxBootID   = "018f2d5a-7b31-7d98-bf8e-3c2f35a1c001"
	outboxBatchA   = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b001"
	outboxBatchB   = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b002"
)

func outboxEnvelope(batchID string, sequence uint64) model.IngestEnvelopeV2 {
	return model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        outboxDeviceID,
		BootID:          outboxBootID,
		BatchID:         batchID,
		Sequence:        sequence,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindEvents,
		Events: []model.Event{{
			EventID: "event-" + batchID,
			TS:      1_785_319_948_062,
			Device:  outboxDeviceID,
		}},
	}
}

func openTestOutbox(t *testing.T, path string, maxBytes int64) *Outbox {
	t.Helper()
	outbox, err := OpenOutbox(path, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	return outbox
}

func ackFor(envelope model.IngestEnvelopeV2) model.IngestAckV2 {
	return model.IngestAckV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        envelope.DeviceID,
		BatchID:         envelope.BatchID,
		AckSequence:     envelope.Sequence,
		Rejected:        []model.IngestRejection{},
		ServerTime:      1_785_319_949_000,
	}
}

func TestOutboxUsesWALAndVersionedSchema(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	var journalMode string
	if err := outbox.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	var version int
	if err := outbox.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != outboxSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, outboxSchemaVersion)
	}
}

func TestOutboxPersistsFIFOAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	first := outboxEnvelope(outboxBatchA, 11)
	second := outboxEnvelope(outboxBatchB, 12)

	outbox, err := OpenOutbox(path, DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	outbox, err = OpenOutbox(path, DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	got, err := outbox.PeekBatch()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("oldest after restart = %#v, want %#v", got, first)
	}
	if err := outbox.Acknowledge(ackFor(first)); err != nil {
		t.Fatal(err)
	}
	got, err = outbox.PeekBatch()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("next after ack = %#v, want %#v", got, second)
	}
}

func TestOutboxSequenceSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := OpenOutbox(path, DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	first := outboxEnvelope(outboxBatchA, 0)
	first.Sequence = 0
	first, err = outbox.EnqueueNext(first)
	if err != nil || first.Sequence != 1 {
		t.Fatalf("first sequence = %d, err=%v", first.Sequence, err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}
	outbox, err = OpenOutbox(path, DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	second := outboxEnvelope(outboxBatchB, 0)
	second.Sequence = 0
	second, err = outbox.EnqueueNext(second)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("sequence after restart = %d, err=%v", second.Sequence, err)
	}
}

func TestOutboxConcurrentEnqueueNextKeepsFIFOSequenceMonotonic(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	const producers = 24
	var wg sync.WaitGroup
	errs := make(chan error, producers)
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			envelope := outboxEnvelope(
				fmt.Sprintf("018f2d5a-7b31-7d98-bf8e-%012x", i+1),
				0,
			)
			envelope.Sequence = 0
			_, err := outbox.EnqueueNext(envelope)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	rows, err := outbox.db.Query(`SELECT envelope FROM outbox_batches ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var want uint64 = 1
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var envelope model.IngestEnvelopeV2
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Sequence != want {
			t.Fatalf("FIFO sequence = %d, want %d", envelope.Sequence, want)
		}
		want++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want != producers+1 {
		t.Fatalf("rows = %d, want %d", want-1, producers)
	}
}

func TestOutboxRejectsProtocolOversizeEnvelopeBeforePersistence(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	envelope := outboxEnvelope(outboxBatchA, 1)
	envelope.Events[0].CWD = string(make([]byte, model.MaxIngestEnvelopeBytes))
	if err := outbox.Enqueue(envelope); !errors.Is(err, ErrEnvelopeTooLarge) {
		t.Fatalf("oversize error = %v, want ErrEnvelopeTooLarge", err)
	}
	stats, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.QueuedBatches != 0 {
		t.Fatalf("oversize envelope persisted: %#v", stats)
	}
}

func TestOutboxEnqueueManyNextIsAtomicUnderCapacityPressure(t *testing.T) {
	probe := openTestOutbox(t, filepath.Join(t.TempDir(), "probe.db"), DefaultOutboxMaxBytes)
	one := outboxEnvelope(outboxBatchA, 1)
	if err := probe.Enqueue(one); err != nil {
		t.Fatal(err)
	}
	probeStats, err := probe.Stats()
	if err != nil {
		t.Fatal(err)
	}

	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), probeStats.QueuedBytes)
	first := outboxEnvelope(outboxBatchA, 0)
	first.Sequence = 0
	second := outboxEnvelope(outboxBatchB, 0)
	second.Sequence = 0
	if _, err := outbox.EnqueueManyNext([]model.IngestEnvelopeV2{first, second}); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("group error = %v, want ErrOutboxFull", err)
	}
	stats, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.QueuedBatches != 0 || stats.QueuedBytes != 0 {
		t.Fatalf("partial group persisted: %#v", stats)
	}
	got, err := outbox.EnqueueNext(first)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 1 {
		t.Fatalf("rolled-back group consumed sequence: got %d, want 1", got.Sequence)
	}
}

func TestOutboxDuplicateEnqueueIsIdempotentButConflictingPayloadFails(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	envelope := outboxEnvelope(outboxBatchA, 1)
	if err := outbox.Enqueue(envelope); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(envelope); err != nil {
		t.Fatalf("exact duplicate enqueue: %v", err)
	}
	stats, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.QueuedBatches != 1 {
		t.Fatalf("queued batches = %d, want 1", stats.QueuedBatches)
	}

	conflict := envelope
	conflict.Sequence = 99
	if err := outbox.Enqueue(conflict); !errors.Is(err, ErrOutboxConflict) {
		t.Fatalf("conflicting duplicate error = %v, want ErrOutboxConflict", err)
	}
}

func TestOutboxAcknowledgeRequiresExactOldestIdentity(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	envelope := outboxEnvelope(outboxBatchA, 42)
	if err := outbox.Enqueue(envelope); err != nil {
		t.Fatal(err)
	}
	valid := ackFor(envelope)

	for _, tc := range []struct {
		name   string
		mutate func(*model.IngestAckV2)
	}{
		{name: "protocol", mutate: func(ack *model.IngestAckV2) { ack.ProtocolVersion = 1 }},
		{name: "device", mutate: func(ack *model.IngestAckV2) { ack.DeviceID = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a099" }},
		{name: "batch", mutate: func(ack *model.IngestAckV2) { ack.BatchID = outboxBatchB }},
		{name: "sequence", mutate: func(ack *model.IngestAckV2) { ack.AckSequence++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := valid
			tc.mutate(&invalid)
			if err := outbox.Acknowledge(invalid); !errors.Is(err, ErrInvalidAcknowledgement) {
				t.Fatalf("error = %v, want ErrInvalidAcknowledgement", err)
			}
			got, err := outbox.PeekBatch()
			if err != nil {
				t.Fatal(err)
			}
			if got.BatchID != envelope.BatchID {
				t.Fatal("invalid acknowledgement deleted the oldest row")
			}
		})
	}

	if err := outbox.Acknowledge(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.PeekBatch(); !errors.Is(err, ErrOutboxEmpty) {
		t.Fatalf("peek after valid ack = %v, want ErrOutboxEmpty", err)
	}
}

func TestOutboxCapacityReturnsBackpressureWithoutDeletingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox := openTestOutbox(t, path, DefaultOutboxMaxBytes)
	first := outboxEnvelope(outboxBatchA, 1)
	if err := outbox.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	initial, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if initial.QueuedBatches != 1 || initial.QueuedBytes <= 0 || initial.OldestQueuedAt <= 0 {
		t.Fatalf("initial stats = %#v", initial)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	full, err := OpenOutbox(path, initial.QueuedBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	if err := full.Enqueue(outboxEnvelope(outboxBatchB, 2)); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("capacity error = %v, want ErrOutboxFull", err)
	}
	stats, err := full.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.QueuedBatches != 1 || stats.QueuedBytes != initial.QueuedBytes || stats.MaxBytes != initial.QueuedBytes {
		t.Fatalf("stats after pressure = %#v, initial=%#v", stats, initial)
	}
	got, err := full.PeekBatch()
	if err != nil || got.BatchID != outboxBatchA {
		t.Fatalf("oldest after pressure = %#v, err=%v", got, err)
	}
}
