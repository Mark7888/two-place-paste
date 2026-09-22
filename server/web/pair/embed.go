// Package webpair holds the pairing hand-off page: the one public screen this
// server serves to a person rather than to a client.
//
// It exists because a QR code carrying a bare base64url string is useless to a
// general-purpose scanner — Google Lens shows it as text and stops. The codes
// this system shows are therefore links into the user's own relay, and this is
// what that link lands on: a page that reads the code out of the fragment and
// hands it to the app.
//
// The assets live in their own package because go:embed cannot reach outside
// the directory of the file that declares it. The package contains no logic on
// purpose — internal/pairlink owns every decision, this owns every byte.
package webpair

import "embed"

// FS holds the hand-off page, its stylesheet and its script.
//
//go:embed page.html style.css app.js
var FS embed.FS
