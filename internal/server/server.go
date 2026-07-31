package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/proxy"
	"github.com/suool/omnitoken/internal/store"
	"github.com/suool/omnitoken/web"
)

type Server struct {
	cfg                   *Config
	store                 *store.Store
	state                 *collect.State
	prices                *pricing.Table
	bcast                 *broadcaster
	now                   func() time.Time
	streamRefreshInterval time.Duration
}

func New(cfg *Config) (*Server, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	state, err := collect.LoadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	prices, err := pricing.Load(cfg.PricingOverrides)
	if err != nil {
		return nil, err
	}
	srv := &Server{cfg: cfg, store: st, state: state, prices: prices, bcast: newBroadcaster()}
	// Apply pricing overrides saved through the settings page (they win over
	// config file entries) before serving anything.
	if err := srv.ReloadPricing(); err != nil {
		return nil, err
	}
	return srv, nil
}

// ResetOffsets makes the next collection pass re-read every local log from the
// start, backfilling derived columns into history (see collect.State).
//
// It is a method on the running server rather than a standalone command
// because the offsets live in memory once the server is up: clearing the file
// underneath a running process would simply be overwritten by its next commit.
// Doing it here, before Run starts the collectors, has no such race.
func (s *Server) ResetOffsets() (int, error) { return s.state.ResetOffsets() }

// startProxy hosts the local API proxy (F14) when configured.
//
// The sink writes straight to the store instead of posting to /api/v1/ingest:
// the events are already inside the process that owns the database, and a
// round trip through its own HTTP endpoint would only add a way to fail. It
// notifies the broadcaster for the same reason the collectors do — the Live
// page must see proxy traffic as it happens (references.md).
func (s *Server) startProxy() {
	if s.cfg.ProxyListen == "" {
		return
	}
	go func() {
		err := proxy.Run(proxy.Config{
			Listen:    s.cfg.ProxyListen,
			Device:    s.cfg.DeviceName,
			Upstreams: s.cfg.ProxyUpstreams,
		}, func(events []model.Event) error {
			inserted, err := s.store.InsertEvents(events, time.Now().UnixMilli())
			if err == nil && inserted > 0 {
				s.bcast.Notify()
			}
			return err
		})
		log.Printf("proxy: %v", err)
	}()
}

func (s *Server) Run() error {
	// Checked before anything starts collecting or listening: a misconfigured
	// server should not have written a row or accepted a request.
	if err := s.requireAuthConsistency(); err != nil {
		return err
	}

	go s.runCollectors()
	s.startProxy()

	if s.cfg.Token == "" {
		log.Printf("WARNING: no ingest token configured (\"token\" in config) — anyone who can reach %s can submit data", s.cfg.Listen)
	}
	mux := s.routes()

	if s.loopbackOnly() {
		log.Printf("omnitoken server listening on %s (db: %s) — 仅本机可访问,读接口免鉴权", s.cfg.Listen, s.cfg.DBPath)
	} else {
		log.Printf("omnitoken server listening on %s (db: %s) — 可被其它机器访问,读写均需 token", s.cfg.Listen, s.cfg.DBPath)
	}
	return newHTTPServer(s.cfg.Listen, mux).ListenAndServe()
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ingest", s.auth(s.handleIngest))
	mux.HandleFunc("POST /api/v2/enroll", s.handleEnrollV2)
	mux.HandleFunc("POST /api/v2/ingest", s.handleIngestV2)
	mux.HandleFunc("POST /api/v2/heartbeat", s.handleHeartbeatV2)
	mux.HandleFunc("POST /api/v2/devices/{device_id}/revoke", s.adminAuth(s.handleRevokeDeviceV2))
	// Every read goes through readAuth. It is a no-op on a loopback-only
	// server, so wrapping them all costs nothing in the common case and means
	// adding an endpoint cannot accidentally leave one open.
	mux.HandleFunc("GET /api/v1/overview", s.readAuth(s.handleOverview))
	mux.HandleFunc("GET /api/v1/breakdown", s.readAuth(s.handleBreakdown))
	mux.HandleFunc("GET /api/v1/blocks", s.readAuth(s.handleBlocks))
	mux.HandleFunc("GET /api/v1/reports", s.readAuth(s.handleReports))
	mux.HandleFunc("GET /api/v1/events", s.readAuth(s.handleEvents))
	mux.HandleFunc("GET /api/v1/cache", s.readAuth(s.handleCache))
	mux.HandleFunc("GET /api/v1/speed", s.readAuth(s.handleSpeed))
	mux.HandleFunc("GET /api/v1/quota", s.readAuth(s.handleQuota))
	mux.HandleFunc("GET /api/v1/heatmap", s.readAuth(s.handleHeatmap))
	mux.HandleFunc("GET /api/v1/devices", s.readAuth(s.handleDevices))
	mux.HandleFunc("GET /api/v1/models", s.readAuth(s.handleModels))
	mux.HandleFunc("GET /api/v1/settings", s.readAuth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/v1/settings", s.adminAuth(s.handlePutSettings))
	mux.HandleFunc("GET /api/v1/stream", s.readAuthStream(s.handleStream))
	mux.HandleFunc("GET /api/v1/live", s.readAuth(s.handleLive))
	// Health stays open on purpose: it carries no usage data, and it is what a
	// client probes to tell "wrong address" from "wrong token".
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "auth_required": !s.loopbackOnly()})
	})
	// The panel itself is usage data once it loads, but it cannot send a header
	// on the initial navigation — so the shell is served open and every XHR it
	// makes is authenticated. Nothing in the static files is private.
	mux.Handle("GET /", http.FileServerFS(web.FS))
	return mux
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func (s *Server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// auth guards the write endpoints with the shared token, whenever one is set.
//
// Writes and reads are guarded by different rules on purpose. A write with no
// token configured is accepted — that is how a single-machine setup ingests from
// its own agent with zero configuration. Reads use readAuth, which derives the
// requirement from the listen address instead (ADR-0016).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			if !credentialOK(r, s.cfg.Token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// adminAuth guards settings mutation with the independently scoped admin
// credential. An empty credential retains the legacy loopback/no-auth setup.
func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken != "" && !credentialOK(r, s.cfg.AdminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func credentialOK(r *http.Request, token string) bool {
	got := r.Header.Get("Authorization")
	want := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// queryTokenOK accepts the credential as `?access_token=` — for the one endpoint
// where a header is not available.
//
// The browser's EventSource API cannot set headers, so an SSE stream has no other
// way to authenticate. A token in a URL is genuinely worse than one in a header:
// it lands in access logs and in `Referer`. It is therefore accepted on
// /api/v1/stream and nowhere else, so the weaker channel cannot be used to reach
// the other thirteen endpoints. The desktop client does not use it at all — its
// stream runs through Rust, which can set headers (ADR-0014).
func (s *Server) queryTokenOK(r *http.Request) bool {
	got := r.URL.Query().Get("access_token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.ReadToken)) == 1
}

// readAuth guards the read endpoints — but only when the server is reachable
// from somewhere other than this machine (ADR-0016).
//
// Every GET here was unauthenticated by design, and ADR-0008 leaned on that: it
// is why the menubar client stores an address and no token. The premise that
// made it safe was written down at the time — "服务端只听 127.0.0.1". The moment
// a second machine has to reach this server, that premise is gone and fourteen
// endpoints plus the whole panel become readable by anyone who can route to it.
//
// So the rule is derived from the listen address rather than left to a flag:
//
//   - loopback only        → reads stay open. Single-machine setups, which are
//     the common case, keep working with no config at all.
//   - reachable + token    → reads require the token, like ingest.
//   - reachable + no token → refused at startup (see requireAuthConsistency);
//     never served open.
//
// Deriving it means a user cannot accidentally expose the panel by editing one
// line, which is exactly how the old default (`:8787`, all interfaces) would
// have done it.
func (s *Server) readAuth(next http.HandlerFunc) http.HandlerFunc {
	return s.readAuthWith(next, false)
}

// readAuthStream is readAuth plus the query-parameter fallback, for SSE only.
func (s *Server) readAuthStream(next http.HandlerFunc) http.HandlerFunc {
	return s.readAuthWith(next, true)
}

func (s *Server) readAuthWith(next http.HandlerFunc, allowQuery bool) http.HandlerFunc {
	if s.loopbackOnly() {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !credentialOK(r, s.cfg.ReadToken) && !(allowQuery && s.queryTokenOK(r)) {
			// WWW-Authenticate so a browser hitting the panel directly gets a
			// prompt rather than a bare 401 with no way forward.
			w.Header().Set("WWW-Authenticate", `Bearer realm="omnitoken"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// loopbackOnly reports whether cfg.Listen can only be reached from this machine.
//
// An empty host ("" as in ":8787") means every interface, which is the opposite
// of loopback — getting that backwards would silently serve the panel to the
// network, so it is spelled out rather than inferred.
func (s *Server) loopbackOnly() bool {
	host, _, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		// Unparseable: assume the worst rather than the convenient.
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireAuthConsistency refuses to start a server that would serve everyone's
// usage data to the network with no credential.
//
// A warning was the alternative and it is not enough: the previous default was
// `:8787` — every interface — with unauthenticated reads, so the insecure setup
// was what a fresh install got, and a log line nobody reads was the only thing
// standing in front of it. Failing loudly at startup, with the two ways to fix
// it named, is the only version of this that actually protects anybody.
func (s *Server) requireAuthConsistency() error {
	if s.loopbackOnly() {
		return nil
	}
	if s.cfg.Token == "" {
		return fmt.Errorf(
			"listen=%q 可被其它机器访问,但 v1 ingest 路由仍启用且没有配置 token",
			s.cfg.Listen)
	}
	if s.cfg.ReadToken == "" {
		return fmt.Errorf(
			"listen=%q 可被其它机器访问,但没有配置 read_token —— 读接口会把全部用量数据对外公开",
			s.cfg.Listen)
	}
	if s.cfg.AdminToken == "" {
		return fmt.Errorf(
			"listen=%q 可被其它机器访问,但没有配置 admin_token —— settings 写接口缺少管理凭据",
			s.cfg.Listen)
	}
	return nil
}

// authenticateIngestV2 binds the authenticated principal to the device_id in
// the decoded envelope. A credential issued to one device therefore cannot be
// used to submit a batch claiming another device's identity.
func (s *Server) authenticateIngestV2(r *http.Request, envelope model.IngestEnvelopeV2) (store.DeviceRecord, bool, error) {
	credential, ok := bearerCredential(r)
	if !ok {
		return store.DeviceRecord{}, false, nil
	}
	return s.store.AuthenticateDevice(envelope.DeviceID, credential)
}

func bearerCredential(r *http.Request) (string, bool) {
	authorization := r.Header.Get("Authorization")
	separator := strings.IndexByte(authorization, ' ')
	if separator <= 0 || separator == len(authorization)-1 {
		return "", false
	}
	if !strings.EqualFold(authorization[:separator], "Bearer") {
		return "", false
	}
	credential := authorization[separator+1:]
	if strings.ContainsAny(credential, " \t\r\n") {
		return "", false
	}
	return credential, true
}

type ingestRequest struct {
	Events []model.Event         `json:"events"`
	Quotas []model.QuotaSnapshot `json:"quotas"`
	// Procs is a pointer because an empty report is meaningful (ADR-0012):
	// "nothing is running on this device" is data, and must not decode the
	// same as an older agent that reports no process state at all.
	Procs *model.ProcReport `json:"procs"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	inserted, err := s.store.InsertEvents(req.Events, time.Now().UnixMilli())
	if err != nil {
		http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	quotas := 0
	if len(req.Quotas) > 0 {
		if quotas, err = s.store.InsertQuotas(req.Quotas); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	procsChanged := false
	if req.Procs != nil {
		if procsChanged, err = s.store.ApplyProcReport(*req.Procs); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if inserted > 0 || quotas > 0 || procsChanged {
		s.bcast.Notify()
	}
	writeJSON(w, map[string]int{"received": len(req.Events), "inserted": inserted, "quotas": quotas})
}

// handleOverview returns everything the M1 dashboard needs in one call.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekStart := dayStart.AddDate(0, 0, -int((now.Weekday()+6)%7)) // Monday
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	resp := map[string]any{}
	var err error
	put := func(key string, v any, e error) {
		if err == nil && e != nil {
			err = e
			return
		}
		resp[key] = v
	}
	today, e := s.store.Summary(dayStart, end)
	put("today", today, e)
	week, e := s.store.Summary(weekStart, end)
	put("week", week, e)
	month, e := s.store.Summary(monthStart, end)
	put("month", month, e)
	all, e := s.store.Summary(time.UnixMilli(0), end)
	put("all_time", all, e)
	daily, e := s.store.Daily(rangeStart, end)
	put("daily", daily, e)
	for _, dim := range []string{"device", "model", "repo", "provider", "source"} {
		rows, e := s.store.Breakdown(dim, rangeStart, end, 30)
		put("by_"+dim, rows, e)
	}
	// Costs (ADR-0005): real vs equivalent per period; per-model over the range.
	costs := map[string]PeriodCost{}
	for key, start := range map[string]time.Time{"today": dayStart, "week": weekStart, "month": monthStart, "all_time": time.UnixMilli(0)} {
		pc, e := s.periodCost(start, end)
		if err == nil && e != nil {
			err = e
		}
		costs[key] = pc
	}
	rangeUsage, e := s.store.ModelUsage(rangeStart, end)
	if err == nil && e != nil {
		err = e
	}
	_, modelCosts, unpriced := s.costFromUsage(rangeUsage)
	resp["costs"] = costs
	resp["model_costs"] = modelCosts
	resp["unpriced_models"] = unpriced

	// Work time (F8/ADR-0006): dual metrics — union (attention) + sum (agent).
	idle := time.Duration(s.cfg.WorktimeIdleMinutes) * time.Minute
	repoWork, workMatrix, e := s.store.WorkTime(rangeStart, end, idle)
	if err == nil && e != nil {
		err = e
	}
	workByRepo := map[string]store.RepoWork{}
	for _, wr := range repoWork {
		workByRepo[wr.Repo] = wr
	}
	resp["work_by_repo"] = workByRepo
	resp["work_matrix"] = workMatrix

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp["days"] = days
	resp["generated_at"] = now.UnixMilli()
	writeJSON(w, resp)
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	dim := r.URL.Query().Get("by")
	days := queryInt(r, "days", 30)
	now := time.Now()
	from := now.AddDate(0, 0, -days)
	rows, err := s.store.Breakdown(dim, from, now.Add(time.Hour), queryInt(r, "limit", 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"by": dim, "rows": rows})
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
