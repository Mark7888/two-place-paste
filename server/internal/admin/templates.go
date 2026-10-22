package admin

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"

	webadmin "github.com/Mark7888/two-place-paste/server/web/admin"
)

// pageData is what every template renders from.
type pageData struct {
	Title         string
	Authenticated bool
	CSRF          string
	Error         string
	Tokens        []tokenView
	Dashboard     *dashboardView

	// Notice is a one-off confirmation of the form just submitted.
	Notice string

	// Problems lists parts of the page that could not be read.
	Problems []string

	// TagNames are the tags a form can choose from.
	TagNames []string

	Group        *groupDetailView
	TagsPage     *tagsView
	SettingsPage *settingsView
	Confirm      *confirmView
	Audit        []auditView
	AuditLimit   string

	// Nav names the screen the header marks as current: "dashboard",
	// "groups", "tags", "tokens", "audit" or "settings". Empty on the
	// sign-in screen.
	Nav string

	// Centered puts the page in the middle of the viewport rather than at the
	// top of a document column. It is for the sign-in screen, which is one
	// short form and nothing else; every other screen is a document.
	Centered bool
}

// tokenView is one row of the token listing (SPEC §4.4). It carries no
// clipboard data because the server holds none.
type tokenView struct {
	Value   string
	Name    string
	Note    string
	Tag     string
	Created string
	Used    bool
	GroupID string
	URL     string

	// Index numbers the row, so its copy button can point at its URL.
	Index int

	// Status is "unused", "used", "expired" or "group deleted", and
	// StatusClass the tag style it is shown in.
	Status      string
	StatusClass string
	Expired     bool
	Expires     string

	// GroupPath links to the group's page while the group exists.
	GroupPath string

	// QRPath is where this token's QR image is served from. It is built here
	// rather than in the template so the token is escaped as a path segment
	// exactly once, by net/url.
	QRPath string

	// RevokePath is where the revoke form posts. Empty for a used token,
	// which cannot be revoked.
	RevokePath string

	// Fresh marks the token this render was redirected to show: the listing
	// opens its code in a dialog, once, without the user hunting for the row.
	Fresh bool
}

// templateSet holds one parsed template per page. Each page is parsed together
// with the shared base, because both pages define "content" and a single set
// cannot hold two definitions of the same name.
type templateSet struct {
	pages map[string]*template.Template
}

func parseTemplates() (*templateSet, error) {
	pages := []string{
		"login.html", "tokens.html", "dashboard.html", "groups.html", "group.html",
		"tags.html", "settings.html", "audit.html", "confirm.html",
	}
	set := &templateSet{pages: make(map[string]*template.Template, len(pages))}
	for _, name := range pages {
		t, err := template.New(name).ParseFS(webadmin.FS, "base.html", name)
		if err != nil {
			return nil, fmt.Errorf("admin: parse template %s: %w", name, err)
		}
		set.pages[name] = t
	}
	return set, nil
}

// render writes a page. It renders into a buffer first so that a template
// failure produces a clean 500 instead of a half-written 200.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	t, ok := s.templates.pages[page]
	if !ok {
		s.logger.ErrorContext(r.Context(), "unknown admin template", "template", page)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		s.logger.ErrorContext(r.Context(), "render admin page", "template", page, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The admin UI loads one stylesheet and one script from its own origin and
	// nothing else. 'self' rather than a nonce or 'unsafe-inline': every line
	// of script is in /admin/app.js, and there is no inline handler on any
	// page for a policy to have to allow.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
