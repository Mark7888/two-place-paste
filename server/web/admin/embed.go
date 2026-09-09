// Package webadmin holds the admin UI's static assets: Go templates and one
// stylesheet (SPEC §4.4).
//
// The assets live in their own package because go:embed cannot reach outside
// the directory of the file that declares it, and internal/admin is not that
// directory. The package contains no logic on purpose — internal/admin owns
// every decision, this owns every byte.
//
// There is deliberately no JavaScript and no second front-end build: the admin
// surface is one server-rendered screen (ROADMAP P4).
package webadmin

import "embed"

// FS holds the admin UI templates and stylesheet.
//
//go:embed *.html style.css
var FS embed.FS
