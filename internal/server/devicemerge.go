package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/suool/omnitoken/internal/store"
)

// Device identity merge endpoints (ADR-0019).
//
// This is the one place in the server that folds two device identities into
// one, and it exists as an explicit administrative route precisely because no
// automatic path is allowed to do it: nothing in the data can prove that two
// self-reported names are the same machine, so the judgement is the user's and
// the endpoint is the record that they made it.
//
// Both routes go through adminAuth. Preview is a POST rather than a GET because
// device names are arbitrary strings that read badly in a URL, and because
// keeping the two on one shape means the dialog and the execution send the same
// body to the same validation.

const deviceMergeActor = "admin"

type deviceMergeRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Confirm must repeat From exactly. A checkbox or an OK button would be
	// satisfied by the same reflex that picked the wrong row in the first place;
	// typing the name out is one more chance to read what is about to happen.
	Confirm string `json:"confirm"`
}

type deviceMergeResponse struct {
	Plan          store.DeviceMergePlan     `json:"plan"`
	Applied       bool                      `json:"applied"`
	LocalDevice   string                    `json:"local_device"`
	TargetIsLocal bool                      `json:"target_is_local"`
	Warnings      []string                  `json:"warnings"`
	History       []store.DeviceMergeRecord `json:"history,omitempty"`
}

func (s *Server) handleDeviceMergePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeDeviceMergeRequest(w, r)
	if !ok {
		return
	}
	plan, err := s.store.PlanDeviceMerge(req.From, req.To)
	if err != nil {
		writeDeviceMergeError(w, err)
		return
	}
	writeJSON(w, s.deviceMergeResponse(plan, req.To, false, nil))
}

func (s *Server) handleDeviceMerge(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeDeviceMergeRequest(w, r)
	if !ok {
		return
	}
	// Checked before the store is asked anything: a mistyped confirmation is a
	// user who is not sure yet, and they should get the same database back.
	if req.Confirm != req.From {
		http.Error(w, "设备合并:请原样输入被合并设备的完整名称以确认(不可撤销)", http.StatusBadRequest)
		return
	}
	applied, err := s.store.MergeDeviceIdentity(req.From, req.To, deviceMergeActor, s.currentTime().UnixMilli())
	if err != nil {
		writeDeviceMergeError(w, err)
		return
	}
	history, err := s.store.DeviceMergeHistory()
	if err != nil {
		http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Every per-device chart on screen just changed shape.
	s.bcast.Notify()
	writeJSON(w, s.deviceMergeResponse(applied, req.To, true, history))
}

func decodeDeviceMergeRequest(w http.ResponseWriter, r *http.Request) (deviceMergeRequest, bool) {
	var req deviceMergeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, settingsBodyMax)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return req, false
	}
	// Device names are compared byte for byte everywhere else in the system;
	// trimming here would let " suool-mac" merge into a row it does not match.
	return req, true
}

// deviceMergeResponse adds the two things only the server knows: which name
// this machine currently writes under, and therefore whether the merge is
// pointing the right way.
func (s *Server) deviceMergeResponse(plan store.DeviceMergePlan, target string, applied bool,
	history []store.DeviceMergeRecord) deviceMergeResponse {
	local := s.cfg.DeviceName
	resp := deviceMergeResponse{
		Plan: plan, Applied: applied, LocalDevice: local,
		TargetIsLocal: local != "" && target == local,
		Warnings:      []string{}, History: history,
	}
	if local != "" && !resp.TargetIsLocal {
		// Merging away from the name this server writes under is not an error —
		// the user may be consolidating onto a different machine's name — but the
		// next collection pass will file new events under the old name again.
		resp.Warnings = append(resp.Warnings, fmt.Sprintf(
			"本机当前以 %q 入库,合并到 %q 之后新事件仍会落到 %q 名下;若方向反了,请对调 source 与 target。",
			local, target, local))
	}
	if plan.QuotaDropped > 0 {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf(
			"有 %d 条配额快照与目标设备同键(同一时刻、同一窗口),合并时丢弃 source 的那份重复观测;token 计数不受影响。",
			plan.QuotaDropped))
	}
	return resp
}

func writeDeviceMergeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDeviceMergeSameIdentity) || errors.Is(err, store.ErrDeviceMergeUnknownIdentity) {
		http.Error(w, "设备合并:"+err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
}

// localIdentity is what the settings page needs to tell the user whether this
// machine is currently living under two names.
type localIdentity struct {
	Device   string `json:"device"`
	Hostname string `json:"hostname,omitempty"`
	// DuplicateIdentity is the hostname, and only when the database holds
	// self-reported events under both it and the resolved device name — the one
	// fact that proves this machine has two identities (ADR-0019 §7.3). It is
	// never a similarity guess about some other device.
	DuplicateIdentity string `json:"duplicate_identity,omitempty"`
}

func (s *Server) localHostname() string {
	lookup := s.hostname
	if lookup == nil {
		lookup = os.Hostname
	}
	name, err := lookup()
	if err != nil {
		return ""
	}
	return name
}

func (s *Server) localIdentity() (localIdentity, error) {
	id := localIdentity{Device: s.cfg.DeviceName, Hostname: s.localHostname()}
	if id.Hostname == "" || id.Device == "" || id.Hostname == id.Device {
		return id, nil
	}
	names, err := s.store.SelfReportedDevices()
	if err != nil {
		return id, err
	}
	seenDevice, seenHostname := false, false
	for _, name := range names {
		if name == id.Device {
			seenDevice = true
		}
		if name == id.Hostname {
			seenHostname = true
		}
	}
	if seenDevice && seenHostname {
		id.DuplicateIdentity = id.Hostname
	}
	return id, nil
}
