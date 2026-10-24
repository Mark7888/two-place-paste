// Package webadmin holds the built admin UI (SPEC §4.4): the React app in
// server/ui, which its build writes into dist/.
//
// The assets live in their own package because go:embed cannot reach outside
// the directory of the file that declares it, and internal/admin is not that
// directory. The package contains no logic on purpose — internal/admin owns
// every decision, this owns every byte.
package webadmin

import "embed"

// FS holds the built admin UI under "dist": index.html, the bundle under
// assets/, and the favicons.
//
// The "all:" prefix matters: without it the .gitkeep that keeps dist/ in the
// repository is skipped, and a checkout with no UI build would not compile at
// all. With it, a binary built without the UI still runs and serves a page
// that says what is missing (internal/admin: placeholderPage).
//
//go:embed all:dist
var FS embed.FS
