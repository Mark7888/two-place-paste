// Package sensitive decides whether a clipboard change is sensitive: content
// the desktop service must not upload on its own, and must ask about before
// uploading by hand (docs/internal/plans/auto-upload-filters.md).
//
// Three sources can say so, checked in this order:
//
//  1. The OS: the application that copied marked the content as private —
//     a password manager, most often (clipboard.Content.Concealed).
//  2. A built-in detector the user switched on (Detectors).
//  3. One of the user's own filters (Filter).
//
// All of it is local to this device. The flag never travels with an entry,
// and nothing here reaches the relay: the server must not learn that an
// entry exists, let alone that it was a password (SPEC §2.3).
//
// Nothing in this package logs, and nothing it returns carries the content,
// a filter's value or its pattern. A filter's value may be the very secret
// it protects.
package sensitive

import (
	"fmt"
	"strings"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
)

// Source is what marked the content sensitive.
type Source string

// The sources, in the order Classify checks them.
const (
	SourceOS       Source = "os"
	SourceDetector Source = "detector"
	SourceFilter   Source = "filter"
)

// Verdict is Classify's answer.
type Verdict struct {
	Sensitive bool

	// Source says what matched. Empty when nothing did.
	Source Source

	// Reason is shown to the user: the OS reason, a detector's title, or a
	// filter's label or kind. Never the value or pattern that matched.
	Reason string

	// Rule names the detector or filter that matched, for the UI; empty for
	// SourceOS.
	Rule string
}

// Config is what a Classifier is built from.
type Config struct {
	// DetectOS honours the OS marker on the content.
	DetectOS bool

	// Detectors names the built-in detectors that are on. An unknown name is
	// ignored, so a settings file written by a newer version still loads.
	Detectors []string

	// Filters are the user's rules; disabled ones are skipped.
	Filters []Filter

	// Salt is the install-wide salt exact filters are hashed with. Needed
	// only when an enabled exact filter exists.
	Salt []byte
}

type compiled struct {
	f     Filter
	match func(in *input) bool
}

// Classifier decides whether content is sensitive. It is immutable once
// built, so it is safe for concurrent use; a settings change builds a new
// one.
type Classifier struct {
	detectOS  bool
	detectors []Detector
	text      []compiled
	filename  []compiled
	salt      []byte
	exact     bool
}

// New builds a classifier. It refuses an invalid filter rather than skipping
// it: a filter that silently matched nothing would be a protection the user
// believes they have.
func New(cfg Config) (*Classifier, error) {
	c := &Classifier{detectOS: cfg.DetectOS, salt: cfg.Salt}
	for _, name := range cfg.Detectors {
		if d, ok := LookupDetector(name); ok {
			c.detectors = append(c.detectors, d)
		}
	}
	for i, f := range cfg.Filters {
		if !f.Enabled {
			continue
		}
		if err := f.Validate(); err != nil {
			return nil, fmt.Errorf("filter %d: %w", i+1, err)
		}
		m, err := f.compile()
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", i+1, err)
		}
		switch f.Target {
		case TargetText:
			c.text = append(c.text, compiled{f, m})
			if f.Kind == KindExact {
				c.exact = true
			}
		case TargetFilename:
			c.filename = append(c.filename, compiled{f, m})
		}
	}
	if c.exact && len(c.salt) != SaltBytes {
		return nil, fmt.Errorf("%w: exact filters need the install salt", ErrInvalid)
	}
	return c, nil
}

// Empty reports whether the classifier can never mark anything.
func (c *Classifier) Empty() bool {
	return c == nil || (!c.detectOS && len(c.detectors) == 0 && len(c.text) == 0 && len(c.filename) == 0)
}

// Classify returns the first match: the OS marker, then detectors, then
// filters. A nil classifier marks nothing.
func (c *Classifier) Classify(content clipboard.Content) Verdict {
	if c == nil {
		return Verdict{}
	}
	if c.detectOS && content.Concealed != "" {
		return Verdict{Sensitive: true, Source: SourceOS, Reason: content.Concealed}
	}
	ms := c.matches(content, true)
	if len(ms) == 0 {
		return Verdict{}
	}
	return ms[0]
}

// Matches returns every detector and filter that matches, OS marker first.
// It is what the filter editor's test box shows.
func (c *Classifier) Matches(content clipboard.Content) []Verdict {
	if c == nil {
		return nil
	}
	var out []Verdict
	if c.detectOS && content.Concealed != "" {
		out = append(out, Verdict{Sensitive: true, Source: SourceOS, Reason: content.Concealed})
	}
	return append(out, c.matches(content, false)...)
}

func (c *Classifier) matches(content clipboard.Content, first bool) []Verdict {
	var out []Verdict
	add := func(v Verdict) bool {
		out = append(out, v)
		return first
	}

	// Text filters and detectors look at text entries only: images and file
	// bodies are not scanned (plan decision 11). The conversion is the one
	// copy of the body this costs.
	if content.Kind() == clipboard.KindText {
		if text := content.Text(); text != "" {
			for _, d := range c.detectors {
				if d.match(text) && add(Verdict{Sensitive: true, Source: SourceDetector, Reason: d.Title, Rule: d.Name}) {
					return out
				}
			}
			in := &input{s: text}
			if c.exact {
				in.exact = HashExact(c.salt, text)
			}
			for _, m := range c.text {
				if m.match(in) && add(filterVerdict(m.f)) {
					return out
				}
			}
		}
	}
	if content.Filename != "" {
		in := &input{s: content.Filename}
		for _, m := range c.filename {
			if m.match(in) && add(filterVerdict(m.f)) {
				return out
			}
		}
	}
	return out
}

func filterVerdict(f Filter) Verdict {
	return Verdict{Sensitive: true, Source: SourceFilter, Reason: f.Reason(), Rule: f.ID}
}

// NewID returns a random filter id.
func NewID() (string, error) {
	s, err := NewSalt()
	if err != nil {
		return "", fmt.Errorf("generate a filter id: %w", err)
	}
	return strings.NewReplacer("+", "", "/", "", "=", "").Replace(s), nil
}
