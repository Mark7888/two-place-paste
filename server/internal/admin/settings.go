package admin

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// maxStaleDays bounds the stale threshold at ten years: past that the setting
// means "never", which an admin can get by not deleting stale groups.
const maxStaleDays = 3650

// settingsJSON is the settings screen.
type settingsJSON struct {
	StaleDays   int `json:"stale_days"`
	DefaultDays int `json:"default_days"`
	MaxDays     int `json:"max_days"`
}

func (s *Server) settingsView(st store.Settings) settingsJSON {
	return settingsJSON{
		StaleDays:   wholeDays(st.StaleAfter),
		DefaultDays: wholeDays(store.DefaultStaleAfter),
		MaxDays:     maxStaleDays,
	}
}

// handleSettings returns the settings screen.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, _ session) {
	writeJSON(w, http.StatusOK, s.settingsView(s.settings(r.Context())))
}

// handleSaveSettings stores the settings and returns them as saved.
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request, _ session) {
	// A float rather than an int, so that 1.5 is refused with the same
	// message as 0 rather than as a request that could not be read.
	var req struct {
		StaleDays float64 `json:"stale_days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	n := req.StaleDays
	if n != math.Trunc(n) || n < 1 || n > maxStaleDays {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Enter a whole number of days from 1 to %d.", maxStaleDays))
		return
	}
	st := store.Settings{StaleAfter: time.Duration(n) * 24 * time.Hour}
	if err := s.store.PutSettings(r.Context(), st); err != nil {
		s.logger.ErrorContext(r.Context(), "save settings", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The settings could not be saved.")
		return
	}
	s.audit(r, "settings.update", "", "stale after "+days(st.StaleAfter))
	writeJSON(w, http.StatusOK, s.settingsView(st))
}
