package tppclient

import "testing"

// operatorLinkCases are the link-building cases. The TypeScript client tests
// the same list (mobile/src/core/protocol/__tests__/urls.test.ts); a change
// here belongs there too.
var operatorLinkCases = []struct {
	name, base, groupID, want string
	ok                        bool
}{
	{"no query", "https://tpp.example.com/something", "abc", "https://tpp.example.com/something?group_id=abc", true},
	{"bare host", "https://tpp.example.com", "abc", "https://tpp.example.com?group_id=abc", true},
	{"trailing slash", "http://192.168.1.10:8080/", "abc", "http://192.168.1.10:8080/?group_id=abc", true},
	{"existing query", "https://example.com/help?lang=en", "abc", "https://example.com/help?lang=en&group_id=abc", true},
	{"empty query", "https://example.com/help?", "abc", "https://example.com/help?group_id=abc", true},
	{"query ending in &", "https://example.com/help?lang=en&", "abc", "https://example.com/help?lang=en&group_id=abc", true},
	{"fragment kept last", "https://example.com/help#contact", "abc", "https://example.com/help?group_id=abc#contact", true},
	{"query and fragment", "https://example.com/help?lang=en#contact", "abc", "https://example.com/help?lang=en&group_id=abc#contact", true},
	{"base64url id unchanged", "https://example.com/", "hFps-imP_eNz9", "https://example.com/?group_id=hFps-imP_eNz9", true},
	{"id escaped", "https://example.com/", "a b&c=d/é", "https://example.com/?group_id=a%20b%26c%3Dd%2F%C3%A9", true},
	{"upper-case scheme", "HTTPS://example.com/", "abc", "HTTPS://example.com/?group_id=abc", true},
	{"no group", "https://example.com/", "", "", false},
	{"empty url", "", "abc", "", false},
	{"bare host without scheme", "tpp.example.com/something", "abc", "", false},
	{"javascript", "javascript:alert(1)", "abc", "", false},
	{"intent", "intent://scan/#Intent;scheme=zxing;end", "abc", "", false},
	{"file", "file:///etc/passwd", "abc", "", false},
	{"no host", "https://", "abc", "", false},
	{"no host before path", "https:///path", "abc", "", false},
	{"port only", "https://:8080/", "abc", "", false},
	{"credentials", "https://user:pw@example.com/", "abc", "", false},
	{"space", "https://example.com/a b", "abc", "", false},
	{"newline", "https://example.com/\n", "abc", "", false},
}

func TestOperatorLink(t *testing.T) {
	t.Parallel()
	for _, tt := range operatorLinkCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := OperatorLink(tt.base, tt.groupID)
			if got != tt.want || ok != tt.ok {
				t.Errorf("OperatorLink(%q, %q) = %q, %v; want %q, %v", tt.base, tt.groupID, got, ok, tt.want, tt.ok)
			}
		})
	}
}
