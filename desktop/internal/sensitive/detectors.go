package sensitive

import (
	"regexp"
	"strings"
)

// Detector is a built-in rule: fixed in code, switched on or off by the user,
// never edited.
type Detector struct {
	// Name is the stable identifier the settings file stores.
	Name string

	// Title is what the user sees, and the reason given for a match.
	Title string

	// Description says in one line what the detector catches.
	Description string

	// DefaultOn is whether the detector is on when the settings file names
	// no detectors.
	DefaultOn bool

	match func(s string) bool
}

// Match reports whether the detector finds anything in s.
func (d Detector) Match(s string) bool { return d.match(s) }

// PEM, OpenSSH, PKCS#8 and PGP private key blocks all open with this armour
// line, and nothing that is not a private key does. The literal prefix lets
// RE2 skip straight to candidates, so this costs a memory scan.
var privateKeyRe = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)

// prefixed is a token with a fixed literal prefix. Matching jumps from one
// occurrence of the prefix to the next and runs an anchored pattern on a
// short window there, instead of trying a pattern at every byte of the text:
// the difference between milliseconds and seconds on a large clipboard.
type prefixed struct {
	prefix string
	re     *regexp.Regexp // anchored with \A; matched against the window
	window int
}

func token(prefix, rest string, window int) prefixed {
	return prefixed{
		prefix: prefix,
		re:     regexp.MustCompile(`\A` + regexp.QuoteMeta(prefix) + rest),
		window: window,
	}
}

// match reports whether the token occurs in s at a word boundary.
func (p prefixed) match(s string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], p.prefix)
		if i < 0 {
			return false
		}
		i += off
		if i == 0 || !wordByte(s[i-1]) {
			if p.re.MatchString(s[i:min(len(s), i+p.window)]) {
				return true
			}
		}
		off = i + 1
	}
}

func matchAny(ps []prefixed) func(string) bool {
	return func(s string) bool {
		for _, p := range ps {
			if p.match(s) {
				return true
			}
		}
		return false
	}
}

// wordByte is what \b treats as a word character, plus the "-" that every
// token alphabet below uses, so a token glued to a longer token is not split.
func wordByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// end requires the token to stop at a word boundary. The window is cut
// at a fixed length, so "or the end of the window" would accept a token that
// merely continues past it; the windows are longer than any real token.
const end = `(?:[^A-Za-z0-9_-]|\z)`

// A JWT is three base64url segments; the first two are JSON objects, so they
// start with "eyJ" ({"). The signature may be empty (alg "none").
var jwtTokens = []prefixed{token("eyJ", `[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]*`, 16<<10)}

// Each is a provider's documented token prefix with enough of the token's
// body after it that a word starting with the prefix in prose does not
// match.
var apiTokens = []prefixed{
	token("AKIA", `[0-9A-Z]{16}`+end, 64),          // AWS access key id
	token("ASIA", `[0-9A-Z]{16}`+end, 64),          // AWS temporary access key id
	token("ghp_", `[A-Za-z0-9]{36,}`, 128),         // GitHub personal token
	token("gho_", `[A-Za-z0-9]{36,}`, 128),         // GitHub OAuth token
	token("ghu_", `[A-Za-z0-9]{36,}`, 128),         // GitHub user-to-server token
	token("ghs_", `[A-Za-z0-9]{36,}`, 128),         // GitHub server-to-server token
	token("ghr_", `[A-Za-z0-9]{36,}`, 128),         // GitHub refresh token
	token("github_pat_", `[A-Za-z0-9_]{22,}`, 256), // GitHub fine-grained token
	token("glpat-", `[A-Za-z0-9_-]{20,}`, 128),     // GitLab personal token
	token("xoxa-", `[A-Za-z0-9-]{10,}`, 256),       // Slack tokens
	token("xoxb-", `[A-Za-z0-9-]{10,}`, 256),
	token("xoxp-", `[A-Za-z0-9-]{10,}`, 256),
	token("xoxr-", `[A-Za-z0-9-]{10,}`, 256),
	token("xoxs-", `[A-Za-z0-9-]{10,}`, 256),
	token("sk_live_", `[A-Za-z0-9]{16,}`, 256),                // Stripe secret key
	token("rk_live_", `[A-Za-z0-9]{16,}`, 256),                // Stripe restricted key
	token("sk-ant-", `[A-Za-z0-9_-]{20,}`, 256),               // Anthropic API key
	token("sk-proj-", `[A-Za-z0-9_-]{20,}`, 256),              // OpenAI project key
	token("AIza", `[0-9A-Za-z_-]{35}`+end, 64),                // Google API key
	token("npm_", `[A-Za-z0-9]{36}`+end, 64),                  // npm token
	token("SG.", `[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`, 128), // SendGrid key
}

// detectors is every built-in rule, in the order the UI lists them.
var detectors = []Detector{
	{
		Name:        "private_keys",
		Title:       "Private keys",
		Description: "PEM, OpenSSH and PGP private key blocks (-----BEGIN … PRIVATE KEY-----).",
		DefaultOn:   true,
		match:       privateKeyRe.MatchString,
	},
	{
		Name:        "jwt",
		Title:       "JSON Web Tokens",
		Description: "Signed session and API tokens of the form eyJ….eyJ….…",
		match:       matchAny(jwtTokens),
	},
	{
		Name:        "api_tokens",
		Title:       "Cloud and API tokens",
		Description: "AWS, GitHub, GitLab, Slack, Stripe, Anthropic, OpenAI, Google, npm and SendGrid keys, by their prefixes.",
		match:       matchAny(apiTokens),
	},
	{
		Name:        "payment_cards",
		Title:       "Payment card numbers",
		Description: "13 to 19 digits that pass the card checksum (Luhn).",
		match:       matchCard,
	},
	{
		Name:        "iban",
		Title:       "IBANs",
		Description: "International bank account numbers that pass the IBAN checksum.",
		match:       matchIBAN,
	},
}

// Detectors returns every built-in detector.
func Detectors() []Detector { return append([]Detector(nil), detectors...) }

// DefaultDetectors names the detectors that are on when the settings file
// names none.
func DefaultDetectors() []string {
	var out []string
	for _, d := range detectors {
		if d.DefaultOn {
			out = append(out, d.Name)
		}
	}
	return out
}

// LookupDetector finds a detector by name.
func LookupDetector(name string) (Detector, bool) {
	for _, d := range detectors {
		if d.Name == name {
			return d, true
		}
	}
	return Detector{}, false
}

// matchCard looks for 13 to 19 digits, optionally grouped by single spaces
// or dashes, standing on their own and passing the Luhn checksum.
//
// It is a byte scanner rather than a regular expression: digits are common
// in ordinary text, and a pattern with counted repetition tried at every one
// of them was the slowest rule on a large clipboard by far.
func matchCard(s string) bool {
	digits := make([]byte, 0, 19)
	for i := 0; i < len(s); {
		if !isDigit(s[i]) || (i > 0 && cardWordByte(s[i-1])) {
			i++
			continue
		}
		// A candidate run starts here: digits with single separators
		// between them.
		digits = digits[:0]
		j := i
		for j < len(s) {
			switch {
			case isDigit(s[j]):
				digits = append(digits, s[j])
				j++
				continue
			case (s[j] == ' ' || s[j] == '-') && j+1 < len(s) && isDigit(s[j+1]):
				j++
				continue
			}
			break
		}
		// The run must end on its own too: "4111…1111abc" is an identifier,
		// not a card.
		if (j == len(s) || !cardWordByte(s[j])) && len(digits) >= 13 && len(digits) <= 19 && luhn(digits) {
			return true
		}
		i = j
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

func isUpperAlnum(c byte) bool { return isDigit(c) || isUpper(c) }

// cardWordByte is \b's notion of a word character.
func cardWordByte(c byte) bool {
	return c == '_' || isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// luhn is the card number checksum: double every second digit from the
// right, and the sum of the digits must be a multiple of ten.
func luhn(digits []byte) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// matchIBAN looks for a country code and two check digits followed by
// letters and digits, optionally grouped by single spaces, that pass the
// IBAN checksum.
//
// Where a group could be the last one — "DE89 3704 … 3000 IBAN" reads on
// into the next word — every group boundary is tried as the end, longest
// first, so a trailing upper-case word does not hide the number before it.
func matchIBAN(s string) bool {
	compact := make([]byte, 0, 34)
	var ends []int
	for i := 0; i+4 <= len(s); i++ {
		if !isUpper(s[i]) || !isUpper(s[i+1]) || !isDigit(s[i+2]) || !isDigit(s[i+3]) {
			continue
		}
		if i > 0 && cardWordByte(s[i-1]) {
			continue
		}
		compact, ends = compact[:0], ends[:0]
		j := i
		for j < len(s) && len(compact) < 34 {
			c := s[j]
			if isUpperAlnum(c) {
				compact = append(compact, c)
				j++
				continue
			}
			if c == ' ' && j+1 < len(s) && isUpperAlnum(s[j+1]) {
				ends = append(ends, len(compact))
				j++
				continue
			}
			break
		}
		if j == len(s) || !cardWordByte(s[j]) {
			ends = append(ends, len(compact))
		}
		for k := len(ends) - 1; k >= 0; k-- {
			n := ends[k]
			if n >= 15 && n <= 34 && ibanChecksum(string(compact[:n])) {
				return true
			}
		}
	}
	return false
}

// ibanChecksum is ISO 13616's mod-97 check: move the first four characters
// to the end, turn letters into numbers (A=10 … Z=35), and the whole number
// must leave a remainder of 1 when divided by 97.
func ibanChecksum(iban string) bool {
	rearranged := iban[4:] + iban[:4]
	rem := 0
	for i := range len(rearranged) {
		c := rearranged[i]
		switch {
		case c >= '0' && c <= '9':
			rem = (rem*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			rem = (rem*100 + int(c-'A'+10)) % 97
		default:
			return false
		}
	}
	return rem == 1
}
