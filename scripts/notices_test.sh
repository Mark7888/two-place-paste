#!/usr/bin/env bash
# Tests for scripts/notices/notices.mjs, each against a throwaway npm project,
# Go module and Maven list.
#
#   scripts/notices_test.sh
#
# Needs node and go. Exits non-zero, naming the failing case, if any
# expectation does not hold.

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/notices/notices.mjs"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0

# contains NAME FILE TEXT: FILE must contain the line TEXT.
contains() {
	if ! grep -qxF -- "$3" "$2"; then
		echo "FAIL $1: no line '$3' in $2" >&2
		failures=$((failures + 1))
	fi
}

# lacks NAME FILE TEXT: FILE must not contain TEXT anywhere.
lacks() {
	if grep -qF -- "$3" "$2"; then
		echo "FAIL $1: '$3' found in $2" >&2
		failures=$((failures + 1))
	fi
}

# fails NAME ARGS…: the script must exit non-zero.
fails() {
	local name=$1
	shift
	if node "$script" "$@" >/dev/null 2>&1; then
		echo "FAIL $name: exited 0, want an error" >&2
		failures=$((failures + 1))
	fi
}

# package DIR NAME VERSION LICENSE [LICENSE-FILE-TEXT]: install a fake package.
package() {
	mkdir -p "$1"
	printf '{"name":"%s","version":"%s","license":"%s"}\n' "$2" "$3" "$4" >"$1/package.json"
	if [[ $# -ge 5 ]]; then
		printf '%s\n' "$5" >"$1/LICENSE"
	fi
}

# --- npm: what ships is listed, dev dependencies are not ----------------------
npm="$work/npm"
mkdir -p "$npm"
cat >"$npm/package-lock.json" <<'JSON'
{
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "app" },
    "node_modules/shipped": { "version": "1.0.0", "license": "MIT" },
    "node_modules/nofile": { "version": "2.0.0", "license": "ISC" },
    "node_modules/tooling": { "version": "3.0.0", "dev": true, "license": "MIT" },
    "node_modules/elsewhere": { "version": "4.0.0", "optional": true },
    "node_modules/shipped/node_modules/nested": { "version": "5.0.0", "license": "BSD-3-Clause" }
  }
}
JSON
package "$npm/node_modules/shipped" shipped 1.0.0 MIT "Copyright (c) Shipped Authors"
package "$npm/node_modules/nofile" nofile 2.0.0 ISC
package "$npm/node_modules/tooling" tooling 3.0.0 MIT "Copyright (c) Tooling"
package "$npm/node_modules/shipped/node_modules/nested" nested 5.0.0 BSD-3-Clause "Copyright (c) Nested"

node "$script" --title "Test notices" --intro "Intro." --npm "$npm" --out "$work/npm.txt" >/dev/null
contains npm-title "$work/npm.txt" "Test notices"
contains npm-intro "$work/npm.txt" "Intro."
contains npm-shipped "$work/npm.txt" "  - shipped 1.0.0 (MIT)"
contains npm-shipped-text "$work/npm.txt" "Copyright (c) Shipped Authors"
contains npm-nested "$work/npm.txt" "  - nested 5.0.0 (BSD-3-Clause)"
contains npm-nofile "$work/npm.txt" "This package ships no license file. Its package.json declares the license ISC; see https://spdx.org/licenses/ for the text."
lacks npm-dev "$work/npm.txt" "tooling"
lacks npm-optional-not-installed "$work/npm.txt" "elsewhere"
contains npm-count "$work/npm.txt" "This file lists the 3 third-party components this build includes, each with its license text."

# A package the lockfile says ships but that is not installed is an error: the
# file would otherwise silently miss it.
rm -rf "$npm/node_modules/nofile"
fails npm-not-installed --title T --npm "$npm" --out "$work/x.txt"
package "$npm/node_modules/nofile" nofile 2.0.0 ISC

# A package with neither a license file nor a license field is an error.
package "$npm/node_modules/nofile" nofile 2.0.0 ""
fails npm-no-license --title T --npm "$npm" --out "$work/x.txt"
package "$npm/node_modules/nofile" nofile 2.0.0 ISC

# --- Maven: the license a POM names, and the full text once ------------------
cat >"$work/maven.json" <<'JSON'
[
  { "coordinates": "androidx.core:core:1.15.0",
    "licenses": [{ "name": "The Apache Software License, Version 2.0", "url": "https://www.apache.org/licenses/LICENSE-2.0.txt" }] },
  { "coordinates": "com.example:quiet:1.0", "licenses": [] }
]
JSON
node "$script" --title T --maven "$work/maven.json" --out "$work/maven.txt" >/dev/null 2>&1
contains maven-listed "$work/maven.txt" "  - androidx.core:core 1.15.0 (Apache-2.0)"
contains maven-points-at-text "$work/maven.txt" "Licensed under The Apache Software License, Version 2.0 (https://www.apache.org/licenses/LICENSE-2.0.txt). The full text of Apache-2.0 is at the end of this file."
contains maven-full-text "$work/maven.txt" "                                 Apache License"
contains maven-no-license "$work/maven.txt" "Its POM declares no license. See the project's own page for its terms."

# --- Go: the standard library is always listed, our own modules never are ----
mod="$work/gomod"
mkdir -p "$mod"
printf 'module github.com/Mark7888/two-place-paste/fake\n\ngo 1.22\n' >"$mod/go.mod"
printf 'package main\n\nimport "fmt"\n\nfunc main() { fmt.Println() }\n' >"$mod/main.go"
GOWORK=off GOFLAGS=-mod=mod node "$script" --title T --go "$mod=." --goos linux,windows --out "$work/go.txt" >/dev/null
contains go-std "$work/go.txt" "  - Go standard library and runtime $(cd "$mod" && GOWORK=off go env GOVERSION) (BSD-3-Clause)"
lacks go-own-module "$work/go.txt" "github.com/Mark7888/two-place-paste/fake"

# --go-optional leaves the Go part out, with a warning, when there is no go.
nogo="$work/nogo"
mkdir -p "$nogo"
ln -s "$(command -v node)" "$nogo/node"
PATH="$nogo" "$nogo/node" "$script" --title T --go "$mod=." --go-optional --npm "$npm" --out "$work/nogo.txt" >/dev/null 2>&1
lacks go-optional "$work/nogo.txt" "Go standard library"
contains go-optional-npm "$work/nogo.txt" "  - shipped 1.0.0 (MIT)"
PATH="$nogo" fails go-required --title T --go "$mod=." --out "$work/x.txt"

if [[ $failures -gt 0 ]]; then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "ok"
