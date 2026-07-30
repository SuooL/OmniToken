package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

func (s *Server) handleIngestV2(w http.ResponseWriter, r *http.Request) {
	envelope, err := decodeIngestV2(w, r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeIngestV2Error(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeIngestV2Error(w, http.StatusBadRequest, "malformed_request")
		return
	}

	receivedAt := time.Now().UnixMilli()
	if rejected := model.ValidateIngestEnvelope(envelope); len(rejected) > 0 {
		writeIngestV2Ack(w, http.StatusUnprocessableEntity, model.IngestAckV2{
			ProtocolVersion: model.IngestProtocolV2,
			DeviceID:        envelope.DeviceID,
			BatchID:         envelope.BatchID,
			AckSequence:     envelope.Sequence,
			Rejected:        rejected,
			ServerTime:      receivedAt,
		})
		return
	}

	_, authenticated, err := s.authenticateIngestV2(r, envelope)
	if err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "authentication_error")
		return
	}
	if !authenticated {
		w.Header().Set("WWW-Authenticate", `Bearer realm="omnitoken-device"`)
		writeIngestV2Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	result, err := s.store.ApplyIngestV2(envelope, receivedAt)
	if errors.Is(err, store.ErrIngestReceiptConflict) {
		writeIngestV2Error(w, http.StatusConflict, "batch_id_conflict")
		return
	}
	if err != nil {
		writeIngestV2Error(w, http.StatusInternalServerError, "store_error")
		return
	}
	if result.Mutated && s.bcast != nil {
		s.bcast.Notify()
	}
	writeIngestV2Ack(w, http.StatusOK, result.Ack)
}

func decodeIngestV2(w http.ResponseWriter, r *http.Request) (model.IngestEnvelopeV2, error) {
	var envelope model.IngestEnvelopeV2
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, model.MaxIngestEnvelopeBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return model.IngestEnvelopeV2{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return model.IngestEnvelopeV2{}, errors.New("multiple JSON values")
		}
		return model.IngestEnvelopeV2{}, err
	}
	return envelope, nil
}

func writeIngestV2Ack(w http.ResponseWriter, status int, ack model.IngestAckV2) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ack)
}

func writeIngestV2Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
