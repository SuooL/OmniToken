package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

const (
	heartbeatV2BodyMax  int64 = 1 << 20
	enrollmentV2BodyMax int64 = 64 << 10
)

type enrollmentV2Request struct {
	DeviceID     string   `json:"device_id"`
	DeviceToken  string   `json:"device_token"`
	DisplayName  string   `json:"display_name"`
	Capabilities []string `json:"capabilities"`
}

func (s *Server) handleEnrollV2(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.AdminToken == "" || !credentialOK(r, s.cfg.AdminToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="omnitoken-admin"`)
		writeIngestV2Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request enrollmentV2Request
	if err := decodeStrictJSON(w, r, enrollmentV2BodyMax, &request); err != nil {
		writeV2RequestError(w, err)
		return
	}
	if !validCanonicalUUID(request.DeviceID) || request.DeviceToken == "" || request.DisplayName == "" {
		writeIngestV2Error(w, http.StatusBadRequest, "invalid_enrollment")
		return
	}

	record, err := s.store.DeviceByID(request.DeviceID)
	switch {
	case errors.Is(err, store.ErrDeviceNotFound):
		record, err = s.store.RegisterDevice(
			request.DeviceID,
			request.DisplayName,
			request.DeviceToken,
			request.Capabilities,
			s.currentTime().UnixMilli(),
		)
		if err != nil {
			writeIngestV2Error(w, http.StatusConflict, "enrollment_conflict")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(record)
		return
	case err != nil:
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}

	_, authenticated, err := s.store.AuthenticateDevice(request.DeviceID, request.DeviceToken)
	if err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	if !authenticated {
		writeIngestV2Error(w, http.StatusConflict, "enrollment_conflict")
		return
	}
	if err := s.store.UpdateDeviceMetadata(request.DeviceID, request.DisplayName, request.Capabilities); err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	record.DisplayName = request.DisplayName
	record.Capabilities = append([]string(nil), request.Capabilities...)
	writeJSON(w, record)
}

func (s *Server) handleHeartbeatV2(w http.ResponseWriter, r *http.Request) {
	var heartbeat model.Heartbeat
	if err := decodeStrictJSON(w, r, heartbeatV2BodyMax, &heartbeat); err != nil {
		writeV2RequestError(w, err)
		return
	}
	if !heartbeatValid(heartbeat) {
		writeIngestV2Error(w, http.StatusBadRequest, "invalid_heartbeat")
		return
	}

	credential, ok := bearerCredential(r)
	if !ok {
		writeHeartbeatUnauthorized(w)
		return
	}
	record, authenticated, err := s.store.AuthenticateDevice(heartbeat.DeviceID, credential)
	if err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "authentication_error")
		return
	}
	if !authenticated {
		writeHeartbeatUnauthorized(w)
		return
	}

	receivedAt := s.currentTime().UnixMilli()
	if heartbeat.ProcessState != nil {
		processState := *heartbeat.ProcessState
		processState.Device = heartbeat.DeviceID
		processState.ObservedAt = receivedAt
		heartbeat.ProcessState = &processState
		if _, err := s.store.ApplyProcReport(processState); err != nil {
			writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
			return
		}
	}
	if err := s.store.SaveHeartbeat(heartbeat, receivedAt); err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	if err := s.store.UpdateDeviceMetadata(record.DeviceID, record.DisplayName, heartbeat.Capabilities); err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	if err := s.store.TouchDevice(record.DeviceID, receivedAt); err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	if s.bcast != nil {
		s.bcast.Notify()
	}
	writeJSON(w, map[string]any{
		"protocol_version": model.IngestProtocolV2,
		"device_id":        record.DeviceID,
		"received_at":      receivedAt,
	})
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeV2RequestError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeIngestV2Error(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	writeIngestV2Error(w, http.StatusBadRequest, "malformed_request")
}

func writeHeartbeatUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="omnitoken-device"`)
	writeIngestV2Error(w, http.StatusUnauthorized, "unauthorized")
}

func heartbeatValid(heartbeat model.Heartbeat) bool {
	if heartbeat.ProtocolVersion != model.IngestProtocolV2 ||
		!validCanonicalUUID(heartbeat.DeviceID) ||
		!validCanonicalUUID(heartbeat.BootID) ||
		heartbeat.Sequence == 0 ||
		heartbeat.SentAt <= 0 ||
		heartbeat.QueuedBatches < 0 ||
		heartbeat.QueuedBytes < 0 {
		return false
	}
	return heartbeat.ProcessState == nil || heartbeat.ProcessState.Device == heartbeat.DeviceID
}

func validCanonicalUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i := range value {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if value[i] != '-' {
				return false
			}
		case value[i] >= '0' && value[i] <= '9':
		case value[i] >= 'a' && value[i] <= 'f':
		default:
			return false
		}
	}
	return true
}
