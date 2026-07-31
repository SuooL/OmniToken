package store

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

const (
	testIngestBatchA = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b001"
	testIngestBatchB = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b002"
	testIngestBoot   = "018f2d5a-7b31-7d98-bf8e-3c2f35a1c001"
)

func testEventEnvelope(batchID string, events ...model.Event) model.IngestEnvelopeV2 {
	return model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testDeviceIDA,
		BootID:          testIngestBoot,
		BatchID:         batchID,
		Sequence:        42,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindEvents,
		Events:          events,
	}
}

func TestOpenMigratesIngestReceipts(t *testing.T) {
	s := openTestStore(t)

	var tableSQL string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'ingest_receipts'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{
		"batch_id TEXT PRIMARY KEY",
		"device_id TEXT NOT NULL",
		"protocol_version INTEGER NOT NULL",
		"ack_sequence TEXT NOT NULL",
		"accepted INTEGER NOT NULL",
		"duplicates INTEGER NOT NULL",
		"server_time INTEGER NOT NULL",
	} {
		if !strings.Contains(tableSQL, constraint) {
			t.Errorf("receipt schema missing %q: %s", constraint, tableSQL)
		}
	}
}

func TestApplyIngestV2EventsCommitsReceiptAndReplaysDeterministically(t *testing.T) {
	s := openTestStore(t)
	existing := model.Event{
		EventID: "existing",
		Device:  testDeviceIDA,
		Source:  "proxy",
	}
	if _, err := s.InsertEvents([]model.Event{existing}, 100); err != nil {
		t.Fatal(err)
	}
	envelope := testEventEnvelope(testIngestBatchA,
		model.Event{
			EventID:   "existing",
			Device:    testDeviceIDA,
			Source:    "codex",
			SessionID: "session-filled-on-duplicate",
		},
		model.Event{
			EventID: "new",
			Device:  testDeviceIDA,
			Source:  "codex",
		},
	)

	result, err := s.ApplyIngestV2(envelope, 1_785_319_949_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replay {
		t.Fatal("first application reported replay")
	}
	if !result.Mutated {
		t.Fatal("insert/fill application must report committed mutation")
	}
	wantAck := model.IngestAckV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testDeviceIDA,
		BatchID:         testIngestBatchA,
		AckSequence:     42,
		Accepted:        1,
		Duplicates:      1,
		Rejected:        []model.IngestRejection{},
		ServerTime:      1_785_319_949_000,
	}
	if !reflect.DeepEqual(result.Ack, wantAck) {
		t.Fatalf("ack = %#v, want %#v", result.Ack, wantAck)
	}

	replayed := envelope
	replayed.Events = []model.Event{{
		EventID: "must-not-be-inserted",
		Device:  testDeviceIDA,
		Source:  "codex",
	}}
	replayResult, err := s.ApplyIngestV2(replayed, 1_785_319_999_999)
	if err != nil {
		t.Fatal(err)
	}
	if !replayResult.Replay || replayResult.Mutated {
		t.Fatalf("replay flags = replay:%v mutated:%v", replayResult.Replay, replayResult.Mutated)
	}
	if !reflect.DeepEqual(replayResult.Ack, wantAck) {
		t.Fatalf("replay ack = %#v, want original %#v", replayResult.Ack, wantAck)
	}

	var eventCount, receiptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_receipts`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 || receiptCount != 1 {
		t.Fatalf("counts after replay = events:%d receipts:%d, want 2/1", eventCount, receiptCount)
	}
	var sessionID, source string
	if err := s.db.QueryRow(`SELECT session_id, source FROM events WHERE event_id = 'existing'`).Scan(&sessionID, &source); err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-filled-on-duplicate" || source != "codex" {
		t.Fatalf("duplicate merge = session:%q source:%q", sessionID, source)
	}
}

func TestApplyIngestV2RollsBackPayloadWhenReceiptWriteFails(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`
		CREATE TRIGGER fail_ingest_receipt
		BEFORE INSERT ON ingest_receipts
		BEGIN
			SELECT RAISE(FAIL, 'receipt failure');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	envelope := testEventEnvelope(testIngestBatchA, model.Event{
		EventID: "rolled-back",
		Device:  testDeviceIDA,
	})

	if _, err := s.ApplyIngestV2(envelope, 1_785_319_949_000); err == nil {
		t.Fatal("receipt failure unexpectedly succeeded")
	}
	var events, receipts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if events != 0 || receipts != 0 {
		t.Fatalf("partial transaction persisted: events=%d receipts=%d", events, receipts)
	}
}

func TestApplyIngestV2SupportsQuotaAndProcessPayloads(t *testing.T) {
	s := openTestStore(t)
	quotaEnvelope := model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testDeviceIDA,
		BootID:          testIngestBoot,
		BatchID:         testIngestBatchA,
		Sequence:        7,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindQuotas,
		Quotas: []model.QuotaSnapshot{{
			Device:        testDeviceIDA,
			Source:        "codex",
			LimitID:       "account",
			Scope:         "weekly",
			WindowMinutes: 10_080,
			UsedPercent:   20,
			ObservedAt:    1_785_319_948_062,
		}},
	}
	quotaResult, err := s.ApplyIngestV2(quotaEnvelope, 1_785_319_949_000)
	if err != nil {
		t.Fatal(err)
	}
	if quotaResult.Ack.Accepted != 1 || quotaResult.Ack.Duplicates != 0 || !quotaResult.Mutated {
		t.Fatalf("quota result = %#v", quotaResult)
	}

	procEnvelope := model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testDeviceIDA,
		BootID:          testIngestBoot,
		BatchID:         testIngestBatchB,
		Sequence:        8,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindProcs,
		Procs: &model.ProcReport{
			Device:     testDeviceIDA,
			ObservedAt: 1_785_319_948_062,
			Sessions: []model.ProcSession{{
				PID:       123,
				Source:    "codex",
				StartedAt: 1_785_319_900_000,
			}},
		},
	}
	procResult, err := s.ApplyIngestV2(procEnvelope, 1_785_319_949_001)
	if err != nil {
		t.Fatal(err)
	}
	if procResult.Ack.Accepted != 1 || procResult.Ack.Duplicates != 0 || !procResult.Mutated {
		t.Fatalf("process result = %#v", procResult)
	}

	quotas, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	running, err := s.RunningSessions(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(quotas) != 1 || len(running) != 1 || running[0].PID != 123 {
		t.Fatalf("stored payloads = quotas:%#v running:%#v", quotas, running)
	}
}

func TestApplyIngestV2ProcessTimestampRefreshWithoutPIDChangeIsNotMutation(t *testing.T) {
	s := openTestStore(t)
	first := model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testDeviceIDA,
		BootID:          testIngestBoot,
		BatchID:         testIngestBatchA,
		Sequence:        1,
		CapturedAt:      1_000,
		Kind:            model.IngestKindProcs,
		Procs: &model.ProcReport{
			Device:     testDeviceIDA,
			ObservedAt: 1_000,
			Sessions: []model.ProcSession{{
				PID:       123,
				Source:    "codex",
				StartedAt: 900,
			}},
		},
	}
	firstResult, err := s.ApplyIngestV2(first, 1_100)
	if err != nil {
		t.Fatal(err)
	}
	if !firstResult.Mutated {
		t.Fatal("initial PID set was not reported as mutation")
	}

	second := first
	second.BatchID = testIngestBatchB
	second.Sequence = 2
	second.CapturedAt = 2_000
	second.Procs = &model.ProcReport{
		Device:     testDeviceIDA,
		ObservedAt: 2_000,
		Sessions: []model.ProcSession{{
			PID:       123,
			Source:    "codex",
			StartedAt: 900,
		}},
	}
	secondResult, err := s.ApplyIngestV2(second, 2_100)
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Mutated {
		t.Fatal("identical PID set was reported as mutation")
	}

	running, err := s.RunningSessions(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].PID != 123 || running[0].ObservedAt != 2_000 {
		t.Fatalf("timestamp refresh did not commit: %#v", running)
	}
	var receipts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 2 {
		t.Fatalf("receipt count = %d, want 2", receipts)
	}
}

func TestApplyIngestV2KnownTextDuplicateIsNotMutation(t *testing.T) {
	s := openTestStore(t)
	event := model.Event{
		EventID:   "known-text",
		Device:    testDeviceIDA,
		Source:    "codex",
		SessionID: "session-known",
	}
	if _, err := s.InsertEvents([]model.Event{event}, 100); err != nil {
		t.Fatal(err)
	}

	result, err := s.ApplyIngestV2(testEventEnvelope(testIngestBatchA, event), 200)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mutated {
		t.Fatal("duplicate carrying only already-known text was reported as mutation")
	}
	if result.Ack.Accepted != 0 || result.Ack.Duplicates != 1 {
		t.Fatalf("ack = %#v, want 0 accepted and 1 duplicate", result.Ack)
	}
}

func TestApplyIngestV2ConcurrentSameBatchHasOnePayloadReceiptAndAck(t *testing.T) {
	s := openTestStore(t)
	envelope := testEventEnvelope(testIngestBatchA, model.Event{
		EventID: "concurrent-event",
		Device:  testDeviceIDA,
		Source:  "codex",
	})

	start := make(chan struct{})
	results := make(chan IngestV2Result, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, receivedAt := range []int64{1_000, 2_000} {
		wg.Add(1)
		go func(receivedAt int64) {
			defer wg.Done()
			<-start
			result, err := s.ApplyIngestV2(envelope, receivedAt)
			results <- result
			errs <- err
		}(receivedAt)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []IngestV2Result
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0].Ack, got[1].Ack) {
		t.Fatalf("concurrent results = %#v, want identical acknowledgements", got)
	}
	mutations, replays := 0, 0
	for _, result := range got {
		if result.Mutated {
			mutations++
		}
		if result.Replay {
			replays++
		}
	}
	if mutations != 1 || replays != 1 {
		t.Fatalf("result flags = mutations:%d replays:%d, want 1/1", mutations, replays)
	}
	var events, receipts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if events != 1 || receipts != 1 {
		t.Fatalf("persisted rows = events:%d receipts:%d, want 1/1", events, receipts)
	}
}

func TestApplyIngestV2RejectsBatchIDOwnedByAnotherDevice(t *testing.T) {
	s := openTestStore(t)
	first := testEventEnvelope(testIngestBatchA, model.Event{
		EventID: "device-a-event",
		Device:  testDeviceIDA,
	})
	if _, err := s.ApplyIngestV2(first, 100); err != nil {
		t.Fatal(err)
	}

	second := testEventEnvelope(testIngestBatchA, model.Event{
		EventID: "device-b-event",
		Device:  testDeviceIDB,
	})
	second.DeviceID = testDeviceIDB
	_, err := s.ApplyIngestV2(second, 200)
	if !errors.Is(err, ErrIngestReceiptConflict) {
		t.Fatalf("error = %v, want ErrIngestReceiptConflict", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_id = 'device-b-event'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("conflicting replay mutated payload")
	}
}
