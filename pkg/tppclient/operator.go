package tppclient

import "strings"

// The relay operator's website (SPEC §4.4). The relay states it as a plain
// base URL in GroupInfoResponse; a client links to it with its group's id
// added, so the operator's site can tell which group came from the app. The
// TypeScript client builds the same link with the same rules
// (mobile/src/core/protocol/urls.ts); both test the same cases.

// OperatorGroupParam is the query parameter that carries the group's id.
const OperatorGroupParam = "group_id"

// ValidOperatorURL reports whether u is a website a client may open: an
// absolute http:// or https:// URL with a host, without credentials, spaces
// or control characters. Anything else — javascript:, intent:, file:, a
// bare host — is never opened, whatever the relay sent.
func ValidOperatorURL(u string) bool {
	lower := strings.ToLower(u)
	var rest string
	switch {
	case strings.HasPrefix(lower, "https://"):
		rest = u[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		rest = u[len("http://"):]
	default:
		return false
	}
	for i := 0; i < len(u); i++ {
		if u[i] <= ' ' || u[i] == 0x7f {
			return false
		}
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return host != "" && !strings.Contains(host, "@") && !strings.HasPrefix(host, ":")
}

// OperatorLink adds groupID to base's query as group_id, keeping any query
// already there and any fragment at the end. It reports false, and returns
// "", when base is not a website a client may open or groupID is empty.
func OperatorLink(base, groupID string) (string, bool) {
	if groupID == "" || !ValidOperatorURL(base) {
		return "", false
	}
	head, frag, hasFrag := strings.Cut(base, "#")
	param := OperatorGroupParam + "=" + escapeQueryValue(groupID)
	switch {
	case !strings.Contains(head, "?"):
		head += "?" + param
	case strings.HasSuffix(head, "?"), strings.HasSuffix(head, "&"):
		head += param
	default:
		head += "&" + param
	}
	if hasFrag {
		head += "#" + frag
	}
	return head, true
}

// escapeQueryValue percent-encodes every byte but the unreserved ones of
// RFC 3986. A group id is base64url and needs none of it; the encoding is
// there so the link stays well-formed whatever the id, and it matches the
// TypeScript client byte for byte (url.QueryEscape would write a space as +).
func escapeQueryValue(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}
