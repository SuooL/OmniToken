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
)

type Config struct {
	ServerURL      string // e.g. http://192.0.2.1:8787 or http://peer:8788 (relay)
	Token          string
	DeviceName     string
	ClaudeDirs     []string
	CodexDirs      []string
	StatePath      string
	Interval       time.Duration
	RelayListen    string            // e.g. ":8788"; empty = relay disabled
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

// RunOnce performs a single scan+report pass (used for backfill and cron).
func (a *Agent) RunOnce() (int, error) {
	specs := collect.LocalSpecs(a.cfg.ClaudeDirs, a.cfg.CodexDirs)
	sink := func(events []model.Event) error {
		collect.RefineProvider(events, a.probe) // local logs only (F9)
		return a.push(events)
	}
	return collect.ScanSources(specs, a.cfg.DeviceName, a.state, collect.LocalRepoResolver, sink, a.pushQuotas)
}

func (a *Agent) Run() error {
	if a.cfg.RelayListen != "" {
		go a.runRelay()
	}
	if a.cfg.ProxyListen != "" {
		go func() {
			err := RunProxy(ProxyConfig{
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
