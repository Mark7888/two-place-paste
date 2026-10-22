package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// maxStaleDays bounds the stale threshold at ten years: past that the setting
// means "never", which an admin can get by not bulk-deleting.
const maxStaleDays = 3650

// settingsView is the settings screen.
type settingsView struct {
	StaleDays   int
	DefaultDays int
}

// handleSettings renders the settings screen.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	s.renderSettings(w, r, sess, http.StatusOK, "")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, sess session, status int, msg string) {
	data := s.page(sess, "Settings", "settings")
	data.Error = msg
	st := s.settings(r.Context())
	data.SettingsPage = &settingsView{
		StaleDays:   int(st.StaleAfter / (24 * time.Hour)),
		DefaultDays: int(store.DefaultStaleAfter / (24 * time.Hour)),
	}
	s.render(w, r, status, "settings.html", data)
}

// handleSaveSettings stores the settings.
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("stale_days")))
	if err != nil || n < 1 || n > maxStaleDays {
		s.renderSettings(w, r, sess, http.StatusBadRequest, fmt.Sprintf("Enter a whole number of days from 1 to %d.", maxStaleDays))
		return
	}
	st := store.Settings{StaleAfter: time.Duration(n) * 24 * time.Hour}
	if err := s.store.PutSettings(r.Context(), st); err != nil {
		s.logger.ErrorContext(r.Context(), "save settings", "error", err)
		s.renderSettings(w, r, sess, http.StatusInternalServerError, "The settings could not be saved.")
		return
	}
	s.audit(r, "settings.update", "", "stale after "+days(st.StaleAfter))
	s.sessions.setNotice(sess.id, "Settings saved.")
	s.redirect(w, r, "/admin/settings")
}
