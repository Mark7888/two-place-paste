package sensitive

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"
)

// Kind is how a filter compares its value with the content.
type Kind string

// The filter kinds.
const (
	// KindContains matches when the value occurs anywhere.
	KindContains Kind = "contains"

	// KindPrefix matches when the trimmed content starts with the value.
	KindPrefix Kind = "prefix"

	// KindSuffix matches when the trimmed content ends with the value.
	KindSuffix Kind = "suffix"

	// KindRegex matches when the RE2 pattern finds a match.
	KindRegex Kind = "regex"

	// KindExact matches when the trimmed text is exactly one secret, which is
	// stored only as a salted argon2id hash (see HashExact).
	KindExact Kind = "exact"
)

// Target is what part of the content a filter looks at.
type Target string

// The filter targets.
const (
	// TargetText is the text of a text entry.
	TargetText Target = "text"

	// TargetFilename is the name of a copied file.
	TargetFilename Target = "filename"
)

// Limits on what a filter list may hold. They keep a hand-edited settings
// file, or a request to the local API, from turning every clipboard change
// into a long computation.
const (
	MaxFilters    = 200
	MaxValueBytes = 1 << 10
	MaxLabelRunes = 100
	maxIDBytes    = 64
)

// Filter is one of the user's own rules. It is also the shape the rules have
// in the settings file.
type Filter struct {
	// ID identifies the filter across edits. Random, assigned when the filter
	// is first saved.
	ID string `json:"id"`

	Kind   Kind   `json:"kind"`
	Target Target `json:"target"`

	// Value is what the filter looks for. Empty for KindExact, whose secret
	// is never stored; Hash holds its digest instead.
	Value string `json:"value,omitempty"`

	// Hash is HashExact's output for a KindExact filter, and empty otherwise.
	Hash string `json:"hash,omitempty"`

	// CaseInsensitive applies to contains, prefix and suffix. A regex says it
	// with (?i); an exact secret is always compared as it is.
	CaseInsensitive bool `json:"case_insensitive"`

	Enabled bool `json:"enabled"`

	// Label is what the user sees as the reason for a match. Optional.
	Label string `json:"label,omitempty"`
}

// ErrInvalid is wrapped by every validation failure, so a caller can answer
// with a client error rather than a server one.
var ErrInvalid = errors.New("sensitive: invalid filter")

// Validate checks one filter. Its messages never quote the value: a filter's
// value may be the very secret the user is protecting.
func (f Filter) Validate() error {
	if f.ID == "" || len(f.ID) > maxIDBytes {
		return fmt.Errorf("%w: missing or overlong id", ErrInvalid)
	}
	switch f.Target {
	case TargetText, TargetFilename:
	default:
		return fmt.Errorf("%w: unknown target %q", ErrInvalid, f.Target)
	}
	if utf8.RuneCountInString(f.Label) > MaxLabelRunes {
		return fmt.Errorf("%w: label is longer than %d characters", ErrInvalid, MaxLabelRunes)
	}
	if len(f.Value) > MaxValueBytes {
		return fmt.Errorf("%w: value is longer than %d bytes", ErrInvalid, MaxValueBytes)
	}
	switch f.Kind {
	case KindContains, KindPrefix, KindSuffix:
		if f.Value == "" {
			return fmt.Errorf("%w: value is empty", ErrInvalid)
		}
	case KindRegex:
		if f.Value == "" {
			return fmt.Errorf("%w: pattern is empty", ErrInvalid)
		}
		if _, err := regexp.Compile(f.Value); err != nil {
			return fmt.Errorf("%w: pattern does not compile: %s", ErrInvalid, regexError(err))
		}
	case KindExact:
		if f.Target != TargetText {
			return fmt.Errorf("%w: an exact match applies to text only", ErrInvalid)
		}
		if f.Value != "" {
			return fmt.Errorf("%w: an exact match keeps only its hash", ErrInvalid)
		}
		if !validHash(f.Hash) {
			return fmt.Errorf("%w: an exact match has no valid hash", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, f.Kind)
	}
	if f.Kind != KindExact && f.Hash != "" {
		return fmt.Errorf("%w: only an exact match has a hash", ErrInvalid)
	}
	return nil
}

// regexError is the compiler's complaint without the pattern. regexp's
// errors are "error parsing regexp: <code>: `<fragment>`", and the fragment is
// part of the pattern; the code alone says what is wrong.
func regexError(err error) string {
	var syn *syntax.Error
	if errors.As(err, &syn) {
		return string(syn.Code)
	}
	return "invalid pattern"
}

// ValidateList checks a whole list: each filter, the count and unique ids.
func ValidateList(fs []Filter) error {
	if len(fs) > MaxFilters {
		return fmt.Errorf("%w: more than %d filters", ErrInvalid, MaxFilters)
	}
	seen := make(map[string]bool, len(fs))
	for i, f := range fs {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("filter %d: %w", i+1, err)
		}
		if seen[f.ID] {
			return fmt.Errorf("filter %d: %w: duplicate id", i+1, ErrInvalid)
		}
		seen[f.ID] = true
	}
	return nil
}

// input is the content one classification looks at, with what more than
// one filter may need computed at most once.
type input struct {
	s string

	// exact is the hash of the trimmed text for exact filters, or "" when no
	// enabled exact filter exists or the text cannot be a secret.
	exact string

	lower   string
	lowered bool
}

// low is s lower-cased, computed on first use and shared by every
// case-insensitive "contains" filter: one copy of a large text, not one per
// filter, and a plain substring search instead of a case-folding regular
// expression tried at every byte.
func (in *input) low() string {
	if !in.lowered {
		in.lower, in.lowered = strings.ToLower(in.s), true
	}
	return in.lower
}

// compile turns a valid filter into a matcher.
func (f Filter) compile() (func(in *input) bool, error) {
	v := f.Value
	switch f.Kind {
	case KindContains:
		if !f.CaseInsensitive {
			return func(in *input) bool { return strings.Contains(in.s, v) }, nil
		}
		lv := strings.ToLower(v)
		return func(in *input) bool { return strings.Contains(in.low(), lv) }, nil
	case KindPrefix:
		if !f.CaseInsensitive {
			return func(in *input) bool { return strings.HasPrefix(strings.TrimSpace(in.s), v) }, nil
		}
		// Anchored at the start, so RE2 stops as soon as the start cannot
		// match: a 50 MB text costs no more than a short one.
		re, err := regexp.Compile(`\A(?i)` + regexp.QuoteMeta(v))
		if err != nil {
			return nil, fmt.Errorf("compile a prefix filter: %w", err)
		}
		return func(in *input) bool { return re.MatchString(strings.TrimSpace(in.s)) }, nil
	case KindSuffix:
		if !f.CaseInsensitive {
			return func(in *input) bool { return strings.HasSuffix(strings.TrimSpace(in.s), v) }, nil
		}
		re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(v) + `\z`)
		if err != nil {
			return nil, fmt.Errorf("compile a suffix filter: %w", err)
		}
		// Case folding maps a rune to one of at most four bytes, so the match
		// lies in the last 4×len(v) bytes. Looking only there keeps a large
		// text from being scanned end to end.
		window := 4 * len(v)
		return func(in *input) bool {
			s := strings.TrimSpace(in.s)
			if len(s) > window {
				s = s[len(s)-window:]
			}
			return re.MatchString(s)
		}, nil
	case KindRegex:
		re, err := regexp.Compile(v)
		if err != nil {
			return nil, fmt.Errorf("compile a regex filter: %w", err)
		}
		return func(in *input) bool { return re.MatchString(in.s) }, nil
	case KindExact:
		h := f.Hash
		return func(in *input) bool { return in.exact != "" && in.exact == h }, nil
	}
	return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalid, f.Kind)
}

// Reason is what the user is shown for a match on this filter: that a filter
// matched, that it is one of their own, and which one — by its label, or by
// its kind when it has none. Never the value.
func (f Filter) Reason() string {
	if f.Label != "" {
		return "Matches custom filter “" + f.Label + "”"
	}
	return "Matches a custom “" + f.KindName() + "” filter"
}

// KindName is the kind in the words the filter editor uses.
func (f Filter) KindName() string {
	switch f.Kind {
	case KindContains:
		return "contains"
	case KindPrefix:
		return "starts with"
	case KindSuffix:
		return "ends with"
	case KindRegex:
		return "regex"
	case KindExact:
		return "exact match"
	}
	return string(f.Kind)
}
