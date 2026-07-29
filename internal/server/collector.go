package server

import (
	"log"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/model"
)

// runCollectors periodically scans the server machine's own logs (push-free
// for the host) and pulls remote hosts over SSH. First run performs the full
// historical backfill automatically because all offsets start at zero.
func (s *Server) runCollectors() {
	interval := time.Duration(s.cfg.Collect.IntervalSeconds) * time.Second
	// SSH pulls are heavier; run them on a slower cadence (min 60s).
	sshEvery := max(4, int(time.Minute/interval))
	// Broadcast hooks the storage layer: the local-collector path must notify
	// SSE subscribers exactly like HTTP ingest does (references.md).
	probe := collect.NewCachedProber(10 * time.Minute)
	sink := func(events []model.Event) error {
		inserted, err := s.store.InsertEvents(events, time.Now().UnixMilli())
		if err == nil && inserted > 0 {
			s.bcast.Notify()
		}
		return err
	}
	localSink := func(events []model.Event) error {
		collect.RefineProvider(events, probe) // this machine's own logs only (F9)
		return sink(events)
	}
	quotaSink := func(qs []model.QuotaSnapshot) error {
		inserted, err := s.store.InsertQuotas(qs)
		if err == nil && inserted > 0 {
			s.bcast.Notify()
		}
		return err
	}
	localSpecs := collect.LocalSpecs(s.cfg.Collect.LocalDirs, s.cfg.Collect.CodexDirs)
	// Claude quota arrives through the status line, not an API call
	// (ADR-0011): Claude Code hands `omnitoken statusline` its own
	// account-level numbers, which get dropped in a file this reads.
	quotaReader := collect.NewStatusQuotaReader(s.cfg.DeviceName, s.cfg.StatuslineCachePath)
	// The server machine collects its own process state the same way an agent
	// does (ADR-0012) — it is just another monitored machine.
	procSink := func() {
		report, err := collect.LiveProcesses(s.cfg.DeviceName, time.Now())
		if err != nil {
			log.Printf("collect[procs]: %v", err)
			return
		}
		changed, err := s.store.ApplyProcReport(report)
		if err != nil {
			log.Printf("collect[procs]: store: %v", err)
			return
		}
		if changed {
			s.bcast.Notify()
		}
	}
	for tick := 0; ; tick++ {
		if s.cfg.LocalEnabled() {
			procSink()
			if qs := quotaReader.Collect(time.Now()); len(qs) > 0 {
				if err := quotaSink(qs); err != nil {
					log.Printf("quota[claude]: store: %v", err)
				}
			}
		}
		if s.cfg.LocalEnabled() {
			n, err := collect.ScanSources(localSpecs, s.cfg.DeviceName, s.state, collect.LocalRepoResolver, localSink, quotaSink)
			if err != nil {
				log.Printf("collect[local]: %v", err)
			} else if n > 0 {
				log.Printf("collect[local]: %d events", n)
			}
		}
		if tick%sshEvery == 0 {
			for _, h := range s.cfg.Collect.SSHHosts {
				mirror, err := collect.SyncSSHHost(h, s.cfg.MirrorRoot)
				if err != nil {
					log.Printf("collect[ssh %s]: %v", h.DeviceName(), err)
					continue
				}
				n, err := collect.ScanSources(collect.MirrorSpecs(mirror), h.DeviceName(), s.state, collect.SSHRepoResolver(h.Host), sink, quotaSink)
				if err != nil {
					log.Printf("collect[ssh %s]: scan: %v", h.DeviceName(), err)
				} else if n > 0 {
					log.Printf("collect[ssh %s]: %d events", h.DeviceName(), n)
				}
			}
		}
		time.Sleep(interval)
	}
}
