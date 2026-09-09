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
}

// tokenView is one row of the token listing (SPEC §4.4). It carries no
// clipboard data because the server holds none.
type tokenView struct {
	Value   string
	Name    string
	Created string
	Used    bool
	GroupID string
	URL     string
}

// templateSet holds one parsed template per page. Each page is parsed together
// with the shared base, because both pages define "content" and a single set
// cannot hold two definitions of the same name.
type templateSet struct {
	pages map[string]*template.Template
}

func parseTemplates() (*templateSet, error) {
	set := &templateSet{pages: make(map[string]*template.Template, 2)}
	for _, name := range []string{"login.html", "tokens.html"} {
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
	// The admin UI loads one stylesheet from its own origin and nothing else.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
