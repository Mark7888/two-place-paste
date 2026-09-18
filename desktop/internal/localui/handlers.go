package localui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// maxRequestBytes bounds a request body. Nothing the UI sends is large: the
// biggest is a pairing payload.
const maxRequestBytes = 64 << 10

// StatusError lets the service choose an HTTP status for a failure the user
// caused — a malformed creation URL, an expired pairing token — so the UI can
// tell "you typed something wrong" from "the service broke".
type StatusError struct {
	Code    int
	Message string
	Err     error
}

func (e *StatusError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *StatusError) Unwrap() error { return e.Err }

// Errorf builds a StatusError.
func Errorf(code int, err error, format string, args ...any) *StatusError {
	return &StatusError{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.api.Status(r.Context()))
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Direction Direction `json:"direction"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Direction == "" {
		body.Direction = DirectionAuto
	}
	switch body.Direction {
	case DirectionAuto, DirectionUpload, DirectionDownload:
	default:
		writeError(w, http.StatusBadRequest, "unknown sync direction")
		return
	}
	res, err := s.api.Sync(r.Context(), body.Direction)
	if err != nil {
		s.fail(w, r, "sync", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := HistoryQuery{}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be a number")
			return
		}
		q.Limit = uint32(n)
	}
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an RFC 3339 timestamp")
			return
		}
		q.Before = t
	}
	page, err := s.api.History(r.Context(), q)
	if err != nil {
		s.fail(w, r, "list history", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleCopyEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryID string `json:"entry_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.EntryID == "" {
		writeError(w, http.StatusBadRequest, "entry_id is required")
		return
	}
	res, err := s.api.CopyEntry(r.Context(), body.EntryID)
	if err != nil {
		s.fail(w, r, "copy an entry", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	roster, err := s.api.Devices(r.Context())
	if err != nil {
		s.fail(w, r, "list devices", err)
		return
	}
	writeJSON(w, http.StatusOK, roster)
}

func (s *Server) handlePrepareRevoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id is required")
		return
	}
	plan, err := s.api.PrepareRevoke(r.Context(), body.DeviceID)
	if err != nil {
		s.fail(w, r, "prepare a revocation", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleConfirmRevoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID string `json:"plan_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.PlanID == "" {
		// SPEC §3.3 step 2: there is no confirmation without the roster the
		// plan carries, so there is no endpoint that takes a device id here.
		writeError(w, http.StatusBadRequest, "plan_id is required")
		return
	}
	roster, err := s.api.ConfirmRevoke(r.Context(), body.PlanID)
	if err != nil {
		s.fail(w, r, "confirm a revocation", err)
		return
	}
	writeJSON(w, http.StatusOK, roster)
}

func (s *Server) handleStartPairing(w http.ResponseWriter, r *http.Request) {
	inv, err := s.api.StartPairing(r.Context())
	if err != nil {
		s.fail(w, r, "start pairing", err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleJoinPairing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Payload string `json:"payload"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Payload == "" {
		writeError(w, http.StatusBadRequest, "payload is required")
		return
	}
	if err := s.api.JoinPairing(r.Context(), body.Payload); err != nil {
		s.fail(w, r, "join a pairing", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStartOffer shows a code for a member to accept. serverURL is the one
// thing the user has to supply in this direction: a device with no group has
// no relay URL either.
func (s *Server) handleStartOffer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerURL string `json:"server_url"`
	}
	if !decode(w, r, &body) {
		return
	}
	view, err := s.api.StartOffer(r.Context(), body.ServerURL)
	if err != nil {
		s.fail(w, r, "show a pairing code", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCancelOffer(w http.ResponseWriter, r *http.Request) {
	if err := s.api.CancelOffer(r.Context()); err != nil {
		s.fail(w, r, "withdraw that pairing code", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePrepareAcceptOffer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}
	plan, err := s.api.PrepareAcceptOffer(r.Context(), body.Code)
	if err != nil {
		s.fail(w, r, "read that pairing code", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleConfirmAcceptOffer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID string `json:"plan_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.PlanID == "" {
		// Accepting hands over the group key, so there is no endpoint here
		// that takes a code: the confirmation dialog's plan id is the only way
		// in (docs/plans/joiner-emitted-pairing.md §5).
		writeError(w, http.StatusBadRequest, "plan_id is required")
		return
	}
	device, err := s.api.ConfirmAcceptOffer(r.Context(), body.PlanID)
	if err != nil {
		s.fail(w, r, "admit that device", err)
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CreationURL string `json:"creation_url"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.CreationURL == "" {
		writeError(w, http.StatusBadRequest, "creation_url is required")
		return
	}
	if err := s.api.CreateGroup(r.Context(), body.CreationURL); err != nil {
		s.fail(w, r, "create a group", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleForget deletes this device's group keys. It takes no body: there is
// nothing to name — a device holds one group key at a time — and the
// confirmation belongs to the screen, which is the only place that can tell
// the user what is and is not removed by it.
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	if err := s.api.Forget(r.Context()); err != nil {
		s.fail(w, r, "leave the group", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.api.Settings(r.Context()))
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var patch SettingsPatch
	if !decode(w, r, &patch) {
		return
	}
	view, err := s.api.UpdateSettings(r.Context(), patch)
	if err != nil {
		s.fail(w, r, "update settings", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// fail turns a service error into a response. A StatusError carries the status
// and a message meant for a person; anything else is a bug and is reported as
// one, with the detail going to the log rather than to the page.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var se *StatusError
	if errors.As(err, &se) {
		s.logger.WarnContext(r.Context(), "a request could not be completed", "op", op, "error", err)
		writeError(w, se.Code, se.Message)
		return
	}
	s.logger.ErrorContext(r.Context(), "a request failed", "op", op, "error", err)
	writeError(w, http.StatusInternalServerError, "the service could not "+op)
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "the request body is not the JSON this endpoint expects")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
