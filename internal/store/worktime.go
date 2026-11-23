package store

import (
	"sort"
	"time"
)

// Work-time derivation (ADR-0006): each usage event is an interval
// [ts-duration, ts]. Within one (device, repo, session) intervals whose gap
// is ≤ idle are bridged (human read/type time counts; longer gaps stop the
// clock). Per repo two orthogonal metrics come out:
//   - UnionSeconds: wall-clock union across all sessions/devices — "how long
//     did I spend on this repo", concurrency never double-counts;
//   - SumSeconds: per-session totals added up — "how much agent activity ran
//     for this repo"; Sum/Union is the parallelism factor.

type RepoWork struct {
	Repo         string `json:"repo"`
	UnionSeconds int64  `json:"union_seconds"`
	SumSeconds   int64  `json:"sum_seconds"`
	Sessions     int    `json:"sessions"`
}

type DeviceRepoWork struct {
	Device       string `json:"device"`
	Repo         string `json:"repo"`
	UnionSeconds int64  `json:"union_seconds"`
}

type span struct{ start, end int64 } // unix ms, start ≤ end

type evPoint struct{ ts, dur int64 }

// bridgeSpans folds one session's event intervals into active spans, bridging
// gaps ≤ idleMS. Intervals are sorted by start time first because events
// ordered by completion timestamp `ts` can have out-of-order `ts - dur` starts
// when a long turn finishes after a shorter concurrent one.
func bridgeSpans(points []evPoint, idleMS int64) []span {
	if len(points) == 0 {
		return nil
	}
	raw := make([]span, len(points))
	for i, p := range points {
		dur := max(p.dur, 0)
		raw[i] = span{start: p.ts - dur, end: p.ts}
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].start == raw[j].start {
			return raw[i].end < raw[j].end
		}
		return raw[i].start < raw[j].start
	})
	out := make([]span, 0, len(raw))
	for _, sp := range raw {
		if n := len(out); n > 0 && sp.start-out[n-1].end <= idleMS {
			if sp.end > out[n-1].end {
				out[n-1].end = sp.end
			}
		} else {
			out = append(out, sp)
		}
	}
	return out
}

// mergeSpans collapses possibly-overlapping spans into disjoint ones, ordered.
// The live view draws these directly, which is why the merged set is returned
// rather than only its total: overlapping bars would misread as extra activity.
func mergeSpans(spans []span) []span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	out := []span{spans[0]}
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp.start > last.end {
			out = append(out, sp)
		} else if sp.end > last.end {
			last.end = sp.end
		}
	}
	return out
}

// unionMS merges possibly-overlapping spans and returns total covered ms.
func unionMS(spans []span) int64 {
	var total int64
	for _, sp := range mergeSpans(spans) {
		total += sp.end - sp.start
	}
	return total
}

func sumMS(spans []span) int64 {
	var t int64
	for _, sp := range spans {
		t += sp.end - sp.start
	}
	return t
}

// WorkTime derives per-repo and per-(device,repo) work metrics from events.
func (s *Store) WorkTime(from, to time.Time, idle time.Duration) ([]RepoWork, []DeviceRepoWork, error) {
	rows, err := s.rdb.Query(
		`SELECT device, repo, session_id, ts, duration_ms FROM events
		 WHERE ts >= ? AND ts < ?
		 ORDER BY device, repo, session_id, ts`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	idleMS := idle.Milliseconds()
	repoSpans := map[string][]span{}
	repoSum := map[string]int64{}
	repoSessions := map[string]int{}
	devRepoSpans := map[[2]string][]span{}

	var curDev, curRepo, curSess string
	var curPoints []evPoint
	first := true
	flush := func() {
		if len(curPoints) == 0 {
			return
		}
		spans := bridgeSpans(curPoints, idleMS)
		repoSpans[curRepo] = append(repoSpans[curRepo], spans...)
		repoSum[curRepo] += sumMS(spans)
		repoSessions[curRepo]++
		k := [2]string{curDev, curRepo}
		devRepoSpans[k] = append(devRepoSpans[k], spans...)
		curPoints = curPoints[:0]
	}
	for rows.Next() {
		var dev, repo, sess string
		var ts, dur int64
		if err := rows.Scan(&dev, &repo, &sess, &ts, &dur); err != nil {
			return nil, nil, err
		}
		if first || dev != curDev || repo != curRepo || sess != curSess {
			flush()
			curDev, curRepo, curSess, first = dev, repo, sess, false
		}
		curPoints = append(curPoints, evPoint{ts, dur})
	}
	flush()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var repos []RepoWork
	for repo, spans := range repoSpans {
		repos = append(repos, RepoWork{
			Repo:         repo,
			UnionSeconds: unionMS(spans) / 1000,
			SumSeconds:   repoSum[repo] / 1000,
			Sessions:     repoSessions[repo],
		})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].UnionSeconds > repos[j].UnionSeconds })

	var matrix []DeviceRepoWork
	for k, spans := range devRepoSpans {
		matrix = append(matrix, DeviceRepoWork{Device: k[0], Repo: k[1], UnionSeconds: unionMS(spans) / 1000})
	}
	sort.Slice(matrix, func(i, j int) bool { return matrix[i].UnionSeconds > matrix[j].UnionSeconds })
	return repos, matrix, nil
}
