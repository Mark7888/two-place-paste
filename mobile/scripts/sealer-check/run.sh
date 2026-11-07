#!/bin/sh
# Checks the native sealer against the spec vectors and @noble/ciphers.
# Usage: npm run sealer-check (from mobile/). Needs node and a JDK.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
node "$here/gen.mjs" "$work/cases.txt"
mkdir -p "$work/src/com/twoplacepaste"
cp "$here/../../android/app/src/main/java/com/twoplacepaste/XChaCha20Poly1305Sealer.java" "$here/SealerCheck.java" "$work/src/com/twoplacepaste/"
javac -d "$work/out" "$work"/src/com/twoplacepaste/*.java
java -cp "$work/out" com.twoplacepaste.SealerCheck "$work/cases.txt"
