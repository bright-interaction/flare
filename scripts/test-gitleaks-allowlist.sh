#!/usr/bin/env bash
# test-gitleaks-allowlist.sh - watch the allowlist refuse before trusting it.
#
# .gitleaks.toml used to exempt internal/ai/ai_test.go and
# internal/api/sensitive_test.go BY PATH, entire files, which are exactly the
# files where somebody reproducing a production leak pastes the real credential.
# It is keyed on synthetic MARKERS now.
#
# That change is only worth anything if an unmarked credential is actually
# reported, and the first attempt at it was NOT: gitleaks v8.30.0 resolves
# `paths` and `regexes` as OR even with condition = "AND", so a path entry
# silently restored the whole-file exemption. This plants the secret and checks.
set -euo pipefail

cd "$(dirname "$0")/.."
GITLEAKS="${GITLEAKS:-gitleaks}"
if ! command -v "$GITLEAKS" >/dev/null 2>&1; then
  echo "skip: gitleaks not installed"
  exit 0
fi

scan() { "$GITLEAKS" detect --source . --config .gitleaks.toml --no-git --no-banner --redact 2>&1 || true; }

# The tree as committed must be clean, or the planted-secret assertion below
# cannot tell its own finding from a pre-existing one.
# Count "no leaks found", not "leaks found": the latter is a SUBSTRING of the
# former, so the clean case counted as dirty. Same family as the `grep -q`
# pipefail trap in CLAUDE.md 15b, and the same lesson: assert on the content you
# actually mean.
before="$(scan)"
n="$(printf '%s\n' "$before" | grep -c 'no leaks found' || true)"
if [ "$n" -eq 0 ]; then
  echo "FAIL: the working tree already has findings, so this test proves nothing"
  printf '%s\n' "$before"
  exit 1
fi
echo "ok: clean tree"

# Plant an unmarked credential in the file the old config exempted wholesale.
TARGET="internal/scan/rules_test.go"
cp "$TARGET" "$TARGET.bak"
trap 'mv -f "$TARGET.bak" "$TARGET"' EXIT
# Assembled at runtime. Writing the literal here would make THIS file a
# finding, which is the guard tripping over its own test data (the estate has
# shipped that shape before: a gate that refused its own source).
PLANTED="sk""_live_""ABCDEFGHIJKLMNOP"
printf '\nvar plantedByAllowlistTest = "%s"\n' "$PLANTED" >> "$TARGET"

after="$(scan)"
n="$(printf '%s\n' "$after" | grep -c 'no leaks found' || true)"
if [ "$n" -gt 0 ]; then
  echo "FAIL: an UNMARKED credential in a scrubber test file was not reported."
  echo "      The allowlist is exempting the file rather than the marker."
  exit 1
fi
echo "ok: refused an unmarked credential in a fixture file"
echo "PASS"
