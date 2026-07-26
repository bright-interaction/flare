#!/usr/bin/env bash
# Produce the public fair-code mirror of Flare at github.com/bright-interaction/flare,
# so `go install github.com/bright-interaction/flare/cmd/server@latest` resolves.
#
# Flare is open core: the whole flare/ tree ships in the mirror under the Flare
# Sustainable Use License (fair-code: self-host free, no reselling as a service).
# The commercial overlay (hosted multi-tenant control plane, fleet DSN auto-wiring)
# lives OUTSIDE this repo, so there is no pro layer to strip here (see LICENSING.md).
# This script strips only the estate deploy compose (which names the house proxy
# network) and redacts internal infra hostnames from all history, then secret-scans
# and build-checks the result before any push.
#
# Safe by default: with no --push it produces + checks the filtered tree and prints
# what it WOULD push. --push performs the outward mirror (requires the public repo
# to exist: gh repo create bright-interaction/flare --public).
#
# Pattern (single-branch split-clone + gitleaks gate) mirrors mesh/reactor; see the
# Hive gotcha "mesh-mirror-split-clone-drags-in-monorepo-branch-secrets".
set -euo pipefail

PUSH=0
REMOTE_URL="git@github.com:bright-interaction/flare.git"
PREFIX="flare"
SPLIT_BRANCH="flare-public-split"

# Internal-ONLY files (not app code): stripped from the mirror's entire history.
# Paths are relative to flare/ (the subtree split strips the prefix). The top-level
# docker-compose.yml is the ESTATE deploy config (external web-proxy network +
# an host host comment); public self-hosters use deploy/docker-compose.yml and
# deploy/helm instead, which are generic.
STRIP_PATHS=(
  docker-compose.yml
  # Internal audit reports. They cite maintainer laptop paths, the internal
  # deploy pipeline, and an inventory of findings that are still open against a
  # running instance. Belt and braces: these live outside flare/ now, but a
  # future one dropped in here must never reach the public mirror.
  AUDIT.md
)
# Any AUDIT-*.md at any depth.
STRIP_GLOBS=(
  'regex:.*AUDIT-.*\.md$'
)

for arg in "$@"; do
  case "$arg" in
    --push) PUSH=1 ;;
    --remote=*) REMOTE_URL="${arg#--remote=}" ;;
    -h|--help) echo "usage: $0 [--push] [--remote=git@github.com:org/repo.git]"; exit 0 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

command -v git-filter-repo >/dev/null 2>&1 || {
  echo "error: git-filter-repo is required (pip install git-filter-repo)." >&2; exit 1; }

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
[ -d "$PREFIX" ] || { echo "error: $PREFIX/ not found at $ROOT" >&2; exit 1; }

# Coarse pre-flight secret guard. Shared by every product mirror, in ONE file, so
# it cannot drift again. It did drift: five copies, five different regexes, one of
# which could not fire at all. See scripts/mirror-secret-preflight.sh for both bugs.
# This is the fast pre-check; the gitleaks scan on the filtered clone below is the
# authoritative gate.
# shellcheck source=../../scripts/mirror-secret-preflight.sh
. "$ROOT/scripts/mirror-secret-preflight.sh"
mirror_secret_preflight "$PREFIX" "$ROOT/$PREFIX/scripts/mirror-secret-allowlist.txt"

echo "Splitting $PREFIX/ subtree (history-preserving) into $SPLIT_BRANCH ..."
git branch -D "$SPLIT_BRANCH" >/dev/null 2>&1 || true
git subtree split --prefix="$PREFIX" -b "$SPLIT_BRANCH"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CLONE="$WORK/flare-public"
# --single-branch + --no-tags: the throwaway clone holds ONLY the disjoint flare
# subtree history, never the monorepo's other branches (which carry unrelated
# project CI secrets). The clone == the publish payload, which makes the gitleaks
# scan below authoritative. file:// disables the hardlink path.
echo "Cloning $SPLIT_BRANCH -> $CLONE (single-branch) ..."
git clone --quiet --single-branch --no-tags --branch "$SPLIT_BRANCH" "file://$ROOT" "$CLONE"

if [ "${#STRIP_PATHS[@]}" -gt 0 ] || [ "${#STRIP_GLOBS[@]}" -gt 0 ]; then
  FR_ARGS=()
  for p in "${STRIP_PATHS[@]}"; do FR_ARGS+=(--path "$p"); done
  for g in "${STRIP_GLOBS[@]}"; do FR_ARGS+=(--path-regex "${g#regex:}"); done
  echo "Stripping internal-only paths from all history: ${STRIP_PATHS[*]} ${STRIP_GLOBS[*]}"
  ( cd "$CLONE" && git filter-repo --force --invert-paths "${FR_ARGS[@]}" )
fi

# Fail closed: no audit report may survive into the publish payload.
if find "$CLONE" -name 'AUDIT*.md' -not -path '*/.git/*' | grep -q .; then
  echo "ERROR: an audit report survived the strip step; refusing to publish." >&2
  find "$CLONE" -name 'AUDIT*.md' -not -path '*/.git/*' >&2
  exit 1
fi

# Redact internal infra references from ALL history (file contents + commit
# messages). Distinctive tokens only, so a literal global replace is safe.
REDACT="$WORK/redactions.txt"
{
  echo 'host==>host'
  echo 'web-proxy==>web-proxy'
  echo 'flare.example.com==>flare.example.com'
  echo 's3.example.com==>s3.example.com'
  echo 'Cloud==>Cloud'
  # Synthetic Stripe-key fixtures in older commits of the AI scrubber tests (NOT
  # real: obvious ABCDEF... values). Current source splits these literals so they
  # are non-contiguous (tests still pass); this scrubs the old contiguous forms so
  # GitHub push protection accepts the mirror. The redacted form is too short to
  # match the Stripe detector.
  echo 'sk_live_REDACTED==>sk_live_REDACTED'
  echo 'sk_live_REDACTED==>sk_live_REDACTED'
} > "$REDACT"
echo "Redacting internal infra hostnames from all history ..."
( cd "$CLONE" && git filter-repo --force --replace-text "$REDACT" --replace-message "$REDACT" )

# Defense in depth: fail if a stripped path survived.
for p in "${STRIP_PATHS[@]}"; do
  [ -e "$CLONE/$p" ] && { echo "REFUSING: stripped path '$p' still present." >&2; exit 1; }
done

echo "Build-checking the mirror ..."
if command -v go >/dev/null 2>&1; then
  ( cd "$CLONE" && go build ./... ) && echo "  builds standalone: OK"
  ( cd "$CLONE" && go test -run='^$' ./... >/dev/null ) && echo "  tests compile: OK"
else
  echo "  (go not found; skipping build check)" >&2
fi

# Authoritative secret scan: the single-branch clone IS the publish payload.
if command -v gitleaks >/dev/null 2>&1; then
  echo "Scanning mirror history for secrets (gitleaks) ..."
  if ! ( cd "$CLONE" && gitleaks detect --source . --config .gitleaks.toml --no-banner --redact >/dev/null 2>&1 ); then
    echo "REFUSING: gitleaks found a secret in the mirror history:" >&2
    ( cd "$CLONE" && gitleaks detect --source . --config .gitleaks.toml --no-banner --redact ) >&2 || true
    exit 1
  fi
  echo "  no secrets in mirror history: OK"
else
  echo "  WARNING: gitleaks not installed; the secret-scan gate is SKIPPED." >&2
  echo "  Install it before pushing: brew install gitleaks" >&2
  [ "$PUSH" -eq 1 ] && { echo "REFUSING to --push without the gitleaks gate." >&2; exit 1; }
fi

if [ "$PUSH" -eq 0 ]; then
  echo; echo "DRY RUN. Filtered mirror ready at: $CLONE"
  echo "Would push its HEAD -> $REMOTE_URL main"
  echo "Re-run with --push once the public repo exists (gh repo create bright-interaction/flare --public)."
  trap - EXIT  # keep $WORK so the operator can inspect the dry-run tree
  exit 0
fi

echo "Pushing filtered mirror -> $REMOTE_URL main ..."
( cd "$CLONE" && git push "$REMOTE_URL" HEAD:main )
echo "Done. Cleanup: git branch -D $SPLIT_BRANCH"
