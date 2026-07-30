// Package agent implements push-mode collection: scan local logs, report to
// the server (or a relay peer) over HTTP. Offsets only advance after the
// upstream accepts a batch, so the log files themselves act as the retry
// buffer — no local spool is needed and no event is ever lost or duplicated.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/proxy"
)

type Config struct {
	ServerURL   string // e.g. http://192.0.2.1:8787 or http://peer:8788 (relay)
	Token       string
	DeviceName  string
	ClaudeDirs  []string
	CodexDirs   []string
	StatePath   string
	Interval    time.Duration
	RelayListen string // e.g. ":8788"; empty = relay disabled
	// Since is the first instant to report; the zero time means no window.
	Since          time.Time
	ProxyListen    string            // e.g. "127.0.0.1:8899"; empty = proxy disabled
	ProxyUpstreams map[string]string // prefix -> upstream base
}

type Agent struct {
	cfg    Config
	state  *collect.State
	client *http.Client
	probe  func() string
}

func New(cfg Config) (*Agent, error) {
	st, err := collect.LoadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	return &Agent{
		cfg:    cfg,
		state:  st,
		client: &http.Client{Timeout: 60 * time.Second},
		probe:  collect.NewCachedProber(10 * time.Minute),
	}, nil
}

// ResetOffsets makes the next pass re-read every log from the start; see
// collect.State.ResetOffsets. Same reason as the server's: the offsets are in
// memory once the agent is running, so this has to happen before the loop.
func (a *Agent) ResetOffsets() (int, error) { return a.state.ResetOffsets() }

// RunOnce performs a single scan+report pass (used for backfill and cron).
func (a *Agent) RunOnce() (int, error) {
	specs := collect.LocalSpecs(a.cfg.ClaudeDirs, a.cfg.CodexDirs)
	sink := func(events []model.Event) error {
		collect.RefineProvider(events, a.probe) // local logs only (F9)
		return a.push(events)
	}
	// A push is a self-report, and on the server side that outranks an
	// observer's guess (ADR-0015) — which is exactly why the window matters
	// here. "An agent only reads its own machine's logs, so everything it finds
	// is this machine's work" is the obvious assumption and it is false whenever
	// a home directory is synced: the agent would then claim another machine's
	// history with full self-report authority. Config `since` bounds that.
	return collect.ScanSources(specs, a.cfg.DeviceName, a.state, collect.LocalRepoResolver, sink, a.pushQuotas, a.cfg.Since)
}

// reportProcs sends this machine's running agent CLIs (ADR-0012).
//
// Kept out of RunOnce, which backfill and cron also call: process state is
// worthless the moment it is stale, so it belongs to the resident loop, not to
// a one-shot historical import. Unlike events it is not retried either — the
// next tick carries a fresher list, and re-sending an old one would tell the
// server that processes which have since exited are still running.
func (a *Agent) reportProcs() error {
	report, err := collect.LiveProcesses(a.cfg.DeviceName, time.Now())
	if err != nil {
		return err
	}
	return a.postIngest(map[string]any{"procs": report})
}

func (a *Agent) Run() error {
	if a.cfg.RelayListen != "" {
		go a.runRelay()
	}
	if a.cfg.ProxyListen != "" {
		go func() {
			err := proxy.Run(proxy.Config{
				Listen:    a.cfg.ProxyListen,
				Device:    a.cfg.DeviceName,
				Upstreams: a.cfg.ProxyUpstreams,
			}, a.push)
			log.Printf("proxy: %v", err)
		}()
	}
	for {
		n, err := a.RunOnce()
		if err != nil {
			log.Printf("agent: report failed (will retry): %v", err)
		} else if n > 0 {
			log.Printf("agent: reported %d events", n)
		}
		if err := a.reportProcs(); err != nil {
			log.Printf("agent: process report failed: %v", err)
		}
		time.Sleep(a.cfg.Interval)
	}
}

func (a *Agent) push(events []model.Event) error {
	return a.postIngest(map[string]any{"events": events})
}

// pushQuotas reports quota snapshots (ADR-0007) through the same endpoint.
func (a *Agent) pushQuotas(qs []model.QuotaSnapshot) error {
	return a.postIngest(map[string]any{"quotas": qs})
}

func (a *Agent) postIngest(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", a.cfg.ServerURL+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("server %s: %s", resp.Status, msg)
	}
	return nil
}

// runRelay forwards ingest requests from peers that cannot reach the server
// directly (chainable: d → c → server). Stateless by design: a failed forward
// returns an error to the downstream agent, which keeps its offsets and
// retries later.
func (a *Agent) runRelay() {
	upstream, err := url.Parse(a.cfg.ServerURL)
	if err != nil {
		log.Printf("relay: bad server url: %v", err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/ingest", proxy)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"relay"}`))
	})
	log.Printf("agent: relaying ingest on %s -> %s", a.cfg.RelayListen, a.cfg.ServerURL)
	if err := http.ListenAndServe(a.cfg.RelayListen, mux); err != nil {
		log.Printf("relay: %v", err)
	}
}
