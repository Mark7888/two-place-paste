package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// parseDotenv reads KEY=VALUE lines from r.
//
// Supported syntax, chosen to match what people actually write in a .env that
// Docker Compose also reads:
//
//	# comment lines and trailing comments after unquoted values
//	KEY=value
//	export KEY=value
//	KEY="double quoted, \n and \" are unescaped"
//	KEY='single quoted, taken literally'
//
// Unquoted values are trimmed of surrounding whitespace. A line without '='
// is a syntax error rather than a silent skip: a typo in an admin password
// line must not degrade into "no password configured".
func parseDotenv(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; sc.Scan(); line++ {
		raw := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")

		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '=' in %q", line, raw)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", line)
		}

		v, err := unquote(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out[key] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read dotenv: %w", err)
	}
	return out, nil
}

func unquote(v string) (string, error) {
	if len(v) < 2 {
		return stripInlineComment(v), nil
	}
	switch q := v[0]; q {
	case '\'':
		if v[len(v)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return v[1 : len(v)-1], nil
	case '"':
		if v[len(v)-1] != '"' {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		body := v[1 : len(v)-1]
		r := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\"`, `"`, `\\`, `\`)
		return r.Replace(body), nil
	default:
		return stripInlineComment(v), nil
	}
}

// stripInlineComment removes a " #..." trailing comment from an unquoted value.
// A '#' that is not preceded by whitespace is kept, so passwords and hashes
// containing '#' survive as long as they are not written with a space before it.
func stripInlineComment(v string) string {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return strings.TrimSpace(v)
}

// loadDotenvFile parses path. A missing file is not an error: running from the
// process environment alone is a supported deployment (ROADMAP P3e ships
// docker-compose, which injects env vars directly).
func loadDotenvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	m, err := parseDotenv(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}
