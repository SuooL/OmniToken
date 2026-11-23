package antigravity

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// stepTiming holds millisecond pure-generation and TTFT intervals extracted
// from Antigravity's read-only sibling conversation metadata DB (ADR-0045).
type stepTiming struct {
	GenMS  int64
	TTFTMS int64
}

// stepTypePlannerResponse is Antigravity's CortexStepType enum for
// PLANNER_RESPONSE steps in conversations/<id>.db.
const stepTypePlannerResponse = 15

// loadConversationTimings reads ONLY step_type=15 timestamp metadata from the
// sibling `<root>/conversations/<session-id>.db` SQLite database (read-only).
// When stepIndices is non-empty, only those step indices are queried so
// incremental scans do not re-read and decode the entire conversation history.
// Prompt text, thinking traces, and tool payloads live in other columns and
// are never selected (N4). Missing or unreadable DBs silently return nil.
func loadConversationTimings(transcriptPath, sessionID string, stepIndices ...int64) map[int64]stepTiming {
	dbPath := conversationDBPath(transcriptPath, sessionID)
	if dbPath == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	uri := "file:" + (&url.URL{Path: filepath.ToSlash(dbPath)}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(1000)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	out := map[int64]stepTiming{}
	scanRows := func(rows *sql.Rows) {
		defer rows.Close()
		for rows.Next() {
			var idx int64
			var blob []byte
			if err := rows.Scan(&idx, &blob); err != nil || len(blob) == 0 {
				continue
			}
			if t, ok := decodeStepMetadataTiming(blob); ok {
				out[idx] = t
			}
		}
	}

	if len(stepIndices) == 0 {
		rows, err := db.Query(`SELECT idx, metadata FROM steps WHERE step_type = ?`, stepTypePlannerResponse)
		if err != nil {
			return nil
		}
		scanRows(rows)
	} else {
		seen := make(map[int64]struct{}, len(stepIndices))
		uniq := make([]int64, 0, len(stepIndices))
		for _, idx := range stepIndices {
			if _, ok := seen[idx]; !ok {
				seen[idx] = struct{}{}
				uniq = append(uniq, idx)
			}
		}
		const chunk = 500
		for start := 0; start < len(uniq); start += chunk {
			part := uniq[start:min(start+chunk, len(uniq))]
			placeholders := strings.Repeat("?,", len(part))
			placeholders = placeholders[:len(placeholders)-1]
			args := make([]any, 1+len(part))
			args[0] = stepTypePlannerResponse
			for i, idx := range part {
				args[i+1] = idx
			}
			rows, err := db.Query(`SELECT idx, metadata FROM steps WHERE step_type = ? AND idx IN (`+placeholders+`)`, args...)
			if err != nil {
				return nil
			}
			scanRows(rows)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// antigravityRoot maps `<root>/brain/<id>/.system_generated/logs/transcript.jsonl`
// to `<root>`.
func antigravityRoot(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	clean := filepath.Clean(transcriptPath)
	// logs -> .system_generated -> <session-id> -> brain -> <root>
	brainDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(clean))))
	if filepath.Base(brainDir) != "brain" {
		return ""
	}
	return filepath.Dir(brainDir)
}

// conversationDBPath maps `<root>/brain/<id>/.system_generated/logs/transcript.jsonl`
// to `<root>/conversations/<id>.db`.
func conversationDBPath(transcriptPath, sessionID string) string {
	root := antigravityRoot(transcriptPath)
	if root == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(root, "conversations", sessionID+".db")
}

type summaryCWDCacheEntry struct {
	modUnixNano int64
	size        int64
	byID        map[string]string
}

var (
	summaryCWDMu    sync.Mutex
	summaryCWDCache = map[string]summaryCWDCacheEntry{}
)

// loadConversationCWD reads ONLY the workspace_uris column for sessionID from
// Antigravity's read-only `<root>/conversation_summaries.db` metadata table,
// cached per (dbPath, mtime, size) so scanning many conversations opens the
// shared DB once per change. Titles, previews, and raw summaries are never
// selected (N4). Missing or unreadable DBs silently return "".
func loadConversationCWD(transcriptPath, sessionID string) string {
	root := antigravityRoot(transcriptPath)
	if root == "" || sessionID == "" {
		return ""
	}
	dbPath := filepath.Join(root, "conversation_summaries.db")
	info, err := os.Stat(dbPath)
	if err != nil {
		return ""
	}
	modNano := info.ModTime().UnixNano()
	size := info.Size()

	summaryCWDMu.Lock()
	entry, ok := summaryCWDCache[dbPath]
	summaryCWDMu.Unlock()
	if ok && entry.modUnixNano == modNano && entry.size == size {
		return entry.byID[sessionID]
	}

	uri := "file:" + (&url.URL{Path: filepath.ToSlash(dbPath)}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(1000)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return ""
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.Query(`SELECT conversation_id, workspace_uris FROM conversation_summaries`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	byID := map[string]string{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			continue
		}
		if p := parseWorkspaceURIs(raw); p != "" {
			byID[id] = p
		}
	}

	summaryCWDMu.Lock()
	summaryCWDCache[dbPath] = summaryCWDCacheEntry{
		modUnixNano: modNano,
		size:        size,
		byID:        byID,
	}
	summaryCWDMu.Unlock()
	return byID[sessionID]
}

func parseWorkspaceURIs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var uris []string
	if err := json.Unmarshal([]byte(raw), &uris); err != nil {
		return ""
	}
	for _, entry := range uris {
		if p := workspaceURIToPath(entry); p != "" {
			return p
		}
	}
	return ""
}

func workspaceURIToPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "file://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		p := u.Path
		if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			p = "//" + u.Host + p
		}
		// Windows file:///C:/path -> C:/path.
		if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'A' && p[1] <= 'Z') || (p[1] >= 'a' && p[1] <= 'z')) && p[2] == ':' {
			p = p[1:]
		}
		if len(p) > 1 && !(len(p) == 3 && p[1] == ':' && p[2] == '/') {
			p = strings.TrimRight(p, "/")
		}
		return p
	}
	if len(raw) > 1 {
		raw = strings.TrimRight(raw, `/\`)
	}
	return raw
}

// decodeStepMetadataTiming extracts the four protobuf google.protobuf.Timestamp
// fields from CortexStepMetadata (f1 = created/request sent, f6 = first token,
// f7 = generation end, f8 = step completed) and computes TTFT and pure
// generation duration without decoding any other field.
func decodeStepMetadataTiming(buf []byte) (stepTiming, bool) {
	var t1, t6, t7, t8 int64
	pos := 0
	for pos < len(buf) {
		key, next, ok := readVarint(buf, pos)
		if !ok {
			return stepTiming{}, false
		}
		pos = next
		field := key >> 3
		wire := key & 7
		switch wire {
		case 0:
			_, next, ok = readVarint(buf, pos)
			if !ok {
				return stepTiming{}, false
			}
			pos = next
		case 1:
			if pos+8 > len(buf) {
				return stepTiming{}, false
			}
			pos += 8
		case 2:
			length, next, ok := readVarint(buf, pos)
			if !ok || length > uint64(len(buf)-next) {
				return stepTiming{}, false
			}
			sub := buf[next : next+int(length)]
			pos = next + int(length)
			switch field {
			case 1:
				t1 = decodeTimestampMS(sub)
			case 6:
				t6 = decodeTimestampMS(sub)
			case 7:
				t7 = decodeTimestampMS(sub)
			case 8:
				t8 = decodeTimestampMS(sub)
			}
		case 5:
			if pos+4 > len(buf) {
				return stepTiming{}, false
			}
			pos += 4
		default:
			return stepTiming{}, false
		}
	}

	var out stepTiming
	if t1 > 0 && t6 > t1 {
		if d := t6 - t1; d <= maxSpanMS {
			out.TTFTMS = d
		}
	}
	end := t7
	if end <= 0 {
		end = t8
	}
	if t6 > 0 && end > t6 {
		if d := end - t6; d <= maxSpanMS {
			out.GenMS = d
		}
	}
	return out, out.GenMS > 0 || out.TTFTMS > 0
}

// decodeTimestampMS decodes a google.protobuf.Timestamp (field 1 = seconds,
// field 2 = nanos) into unix milliseconds.
func decodeTimestampMS(buf []byte) int64 {
	var sec, nanos int64
	pos := 0
	for pos < len(buf) {
		key, next, ok := readVarint(buf, pos)
		if !ok {
			return 0
		}
		pos = next
		field := key >> 3
		wire := key & 7
		if wire != 0 {
			return 0
		}
		val, next, ok := readVarint(buf, pos)
		if !ok {
			return 0
		}
		pos = next
		switch field {
		case 1:
			sec = int64(val)
		case 2:
			nanos = int64(val)
		}
	}
	if sec <= 0 {
		return 0
	}
	return sec*1000 + nanos/1_000_000
}

func readVarint(buf []byte, pos int) (uint64, int, bool) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if pos >= len(buf) {
			return 0, pos, false
		}
		b := buf[pos]
		pos++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, pos, true
		}
	}
	return 0, pos, false
}
