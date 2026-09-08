package store

import (
	"testing"
	"time"
)

// Reads run on a separate pool from writes (ADR-0027). With the old single
// connection (MaxOpenConns(1)) a read could not proceed while the one
// connection was held inside a write transaction — which is exactly what the
// constant ingest writer did, so the overview's ~18 sequential reads queued
// behind it and the panel hung. Holding a write transaction open here and
// requiring a read to still return proves the pools are separated.
func TestReadsDoNotBlockBehindAnOpenWriteTransaction(t *testing.T) {
	s, err := Open(t.TempDir() + "/rw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Occupy the single writer connection with an open, uncommitted write.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO events(event_id, ts) VALUES('held', 1)`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		var n int
		done <- s.rdb.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read on the read pool failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read blocked behind the open write transaction — read/write pools are not separated")
	}
}

// The read pool refuses writes (query_only), so a stray write routed to it
// fails loudly instead of silently becoming a second writer that fights ingest.
func TestReadPoolRejectsWrites(t *testing.T) {
	s, err := Open(t.TempDir() + "/ro.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.rdb.Exec(`INSERT INTO events(event_id, ts) VALUES('x', 1)`); err == nil {
		t.Fatal("write on the read pool succeeded; query_only guard is not in effect")
	}
}
