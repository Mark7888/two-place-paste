#!/bin/sh
# Checks the native sealer and opener against the spec vectors and
# @noble/ciphers, and the native range capture against an independent encoder.
# Usage: npm run sealer-check (from mobile/). Needs node and a JDK.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
node "$here/gen.mjs" "$work/cases.txt" "$work/capture.txt"
mkdir -p "$work/src/com/twoplacepaste"
native="$here/../../android/app/src/main/java/com/twoplacepaste"
cp "$native/XChaCha20Poly1305Sealer.java" "$native/XChaCha20Poly1305Opener.java" "$native/RangeCapture.java" \
  "$here/SealerCheck.java" "$work/src/com/twoplacepaste/"
javac -d "$work/out" "$work"/src/com/twoplacepaste/*.java
java -cp "$work/out" com.twoplacepaste.SealerCheck "$work/cases.txt" "$work/capture.txt"
