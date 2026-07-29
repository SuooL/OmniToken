package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
	"github.com/suool/omnitoken/web"
)

type Server struct {
	cfg    *Config
	store  *store.Store
	state  *collect.State
	prices *pricing.Table
	bcast  *broadcaster
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

func (s *Server) Run() error {
	go s.runCollectors()

	if s.cfg.Token == "" {
		log.Printf("WARNING: no ingest token configured (\"token\" in config) — anyone who can reach %s can submit data", s.cfg.Listen)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ingest", s.auth(s.handleIngest))
	mux.HandleFunc("GET /api/v1/overview", s.handleOverview)
	mux.HandleFunc("GET /api/v1/breakdown", s.handleBreakdown)
	mux.HandleFunc("GET /api/v1/blocks", s.handleBlocks)
	mux.HandleFunc("GET /api/v1/reports", s.handleReports)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.HandleFunc("GET /api/v1/cache", s.handleCache)
	mux.HandleFunc("GET /api/v1/speed", s.handleSpeed)
	mux.HandleFunc("GET /api/v1/quota", s.handleQuota)
	mux.HandleFunc("GET /api/v1/heatmap", s.handleHeatmap)
	mux.HandleFunc("GET /api/v1/devices", s.handleDevices)
	mux.HandleFunc("GET /api/v1/models", s.handleModels)
	mux.HandleFunc("GET /api/v1/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("GET /api/v1/stream", s.handleStream)
	mux.HandleFunc("GET /api/v1/live", s.handleLive)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /", http.FileServerFS(web.FS))

	log.Printf("omnitoken server listening on %s (db: %s)", s.cfg.Listen, s.cfg.DBPath)
	return http.ListenAndServe(s.cfg.Listen, mux)
}

// auth guards ingestion with the shared token (viewing APIs stay open —
// intended for LAN/tailnet exposure, put a reverse proxy in front otherwise).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.cfg.Token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

type ingestRequest struct {
	Events []model.Event         `json:"events"`
	Quotas []model.QuotaSnapshot `json:"quotas"`
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
	if inserted > 0 || quotas > 0 {
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
