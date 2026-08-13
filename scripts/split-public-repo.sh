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

# The three-phase contract is shared by every mirror (CLAUDE.md section 15) and is
# sourced by a path relative to THIS file, because a phase flag decides which of
# the regions below run at all and that decision is needed before `git rev-parse`
# has been asked where the monorepo root is. The push phase in particular runs in
# a container that has no monorepo and therefore no `rev-parse` answer at all.
# shellcheck source=../../scripts/mirror-phases.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/scripts/mirror-phases.sh"

for arg in "$@"; do
  case "$arg" in
    --push) PUSH=1 ;;
    --remote=*) REMOTE_URL="${arg#--remote=}" ;;
    -h|--help) echo "usage: $0 [--push] [--remote=git@github.com:org/repo.git]"; exit 0 ;;
    # Phase flags are consumed by the shared helper. The || refusal is kept so a
    # typo'd flag still stops the publish instead of silently falling through to
    # the default phase, which would run all three regions in one container and
    # undo the split.
    *) mirror_phase_arg "$arg" || { echo "unknown arg: $arg" >&2; exit 2; } ;;
  esac
done

command -v git-filter-repo >/dev/null 2>&1 || {
  echo "error: git-filter-repo is required (pip install git-filter-repo)." >&2; exit 1; }

mirror_phase_reject_push_flag "$PUSH"

# MIRROR_ROOT exists for the push phase, whose container mounts the two shared
# helper scripts it needs and nothing else: there is no monorepo there, so
# `git rev-parse --show-toplevel` has nothing to answer with. Every other phase
# resolves the root the normal way.
ROOT="${MIRROR_ROOT:-$(git rev-parse --show-toplevel)}"
cd "$ROOT"

# Sourced in EVERY phase, push included: the push container mounts the shared
# scripts/ directory precisely so these resolve there, and defining a function
# costs nothing. Only the CALLS below are phase-dependent.
# shellcheck source=../../scripts/mirror-secret-preflight.sh
. "$ROOT/scripts/mirror-secret-preflight.sh"
. "$ROOT/scripts/mirror-enterprise-check.sh"
# shellcheck source=../../scripts/mirror-module-path.sh
. "$ROOT/scripts/mirror-module-path.sh"
# shellcheck source=../../scripts/mirror-redactions.sh
. "$ROOT/scripts/mirror-redactions.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CLONE="$WORK/flare-public"

# ============================ PHASE: prepare =================================
# Decides WHAT is published. Reads the monorepo; runs git, git-filter-repo and
# python and nothing else. Holds no credential and executes no repository build
# code, which is what makes its output trustworthy enough to publish later.
# =============================================================================
if mirror_phase_does_prepare; then
[ -d "$PREFIX" ] || { echo "error: $PREFIX/ not found at $ROOT" >&2; exit 1; }

# Coarse pre-flight secret guard. Shared by every product mirror, in ONE file, so
# it cannot drift again. It did drift: five copies, five different regexes, one of
# which could not fire at all. See scripts/mirror-secret-preflight.sh for both bugs.
# This is the fast pre-check; the gitleaks scan on the filtered clone below is the
# authoritative gate.
mirror_secret_preflight "$PREFIX" "$ROOT/$PREFIX/scripts/mirror-secret-allowlist.txt"

echo "Splitting $PREFIX/ subtree (history-preserving) into $SPLIT_BRANCH ..."
git branch -D "$SPLIT_BRANCH" >/dev/null 2>&1 || true
git subtree split --prefix="$PREFIX" -b "$SPLIT_BRANCH"

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
#
# This gate used to read `find ... | grep -q .`, which fails OPEN under the
# `set -euo pipefail` at the top of this file: grep -q exits on the FIRST
# match, find takes SIGPIPE, the pipeline status is 141, the `if` takes the
# false branch and the gate reports "no audit report survived" exactly when
# there is the most to find. The next line after it is an irreversible public
# force-push. Capture the CONTENT and test that instead; see the "producer |
# grep -q" rule in the repo-root CLAUDE.md.
AUDIT_SURVIVORS="$(find "$CLONE" -name 'AUDIT*.md' -not -path '*/.git/*' || true)"
if [ -n "$AUDIT_SURVIVORS" ]; then
  echo "ERROR: an audit report survived the strip step; refusing to publish." >&2
  printf '%s\n' "$AUDIT_SURVIVORS" >&2
  exit 1
fi

# Redact internal infra references from ALL history (file contents + commit
# messages). Distinctive tokens only, so a literal global replace is safe.
REDACT="$WORK/redactions.txt"
# Redactions come from ONE shared list plus this product's extras, because the
# per-product copies drifted: slab's never got the estate host IP or the internal
# service hostnames, so the production IP sat in its test fixtures labelled "prod
# host" and 98 occurrences of an internal SaaS hostname stayed in its history.
mirror_redaction_file "$ROOT" "$ROOT/flare/scripts/mirror-redactions.txt" "$REDACT"
echo "Redacting internal infra hostnames from all history ..."
( cd "$CLONE" && git filter-repo --force --replace-text "$REDACT" --replace-message "$REDACT" )

# Rewrite authors BEFORE asserting. The redaction check reads COMMIT objects too,
# so it sees the author field; running it first flagged identities that this very
# pass was about to fix. Assertions belong last, over the final state.
mirror_rewrite_authors "$CLONE"

# Assert the redaction actually took. Rewriting a token is a hope; checking it is
# gone is the guarantee, and this walks every blob in every commit because that is
# what the push publishes (mesh found names neutralised at HEAD still present in 61
# of 156 published commits).
mirror_redaction_check "$CLONE" "$ROOT" "$ROOT/flare/scripts/mirror-redactions.txt"

# Blobs that text redaction cannot fix: a committed binary or archive, or a
# maintainer home path baked into build metadata. Nothing at HEAD reveals these.
mirror_blob_sanity_check "$CLONE" "$ROOT/flare/scripts/mirror-blob-allowlist.txt"

# Refuse closed-source code in the mirror, at HEAD and in history. A no-op for
# products that ship no enterprise surface, which is most of them; wired in
# anyway so a product that GAINS one is covered from the first commit rather
# than after someone remembers to back-port the gate.
mirror_enterprise_check "$CLONE" || exit 1

fi
# ========================== end PHASE: prepare ===============================

# In --prepare-only mode this writes the payload out and exits. In every other
# mode it is a no-op.
mirror_phase_export "$CLONE"
# In --gate-only / --push-prepared mode this points $CLONE at the prepared
# payload (a private copy for the gate phase) and proves it is the tree the
# prepare phase built. In "all" mode it is a no-op.
mirror_phase_import



# Defense in depth: fail if a stripped path survived.
for p in "${STRIP_PATHS[@]}"; do
  [ -e "$CLONE/$p" ] && { echo "REFUSING: stripped path '$p' still present." >&2; exit 1; }
done

# ============================= PHASE: gate ===================================
# Decides whether the payload is FIT to push. The build and test steps below run
# every transitive dependency's test code and every frontend build plugin, so
# this is the only phase that executes code we do not control, and therefore the
# only phase that must be assumed hostile. It gets the payload read-only and
# works on a copy; it never sees the bare repo or a credential.
# =============================================================================
if mirror_phase_does_gates; then

echo "Build-checking the mirror ..."
# `cmd && echo OK` does NOT fail the script when cmd fails, even under `set -e`:
# bash exempts every command in an AND-OR list except the last, so a broken build
# printed nothing and the publish sailed on to the push. slab's mirror failed to
# link against DuckDB on musl and this gate said nothing at all; only the push
# being rejected revealed the run was unhealthy. Check the status explicitly.
if command -v go >/dev/null 2>&1; then
  if ( cd "$CLONE" && go build ./... ); then
    echo "  builds standalone: OK"
  else
    echo "REFUSING: the filtered mirror does not build standalone." >&2
    echo "  A public repo that cannot compile is not publishable. Either a stripped path" >&2
    echo "  is still referenced, or the build needs a toolchain this container lacks." >&2
    exit 1
  fi
  # RUN the tests, do not merely compile them. This used to be `go test -run='^$'`,
  # which compiles every _test.go and executes none, and that hole shipped a red
  # suite to the public: the publish redaction rewrites the maintainer's own address
  # (user@example.com ==> user@example.com), which silently inverted a Shield test that
  # asserts a PERSONAL domain is classified as personal, because example.com is a
  # business domain. Compiling proved nothing about it.
  #
  # The filtered tree is the artifact a contributor clones, and redaction, path
  # stripping and commit rewriting can all change its BEHAVIOUR, not just its text.
  # The only honest check is to run it.
  if ( cd "$CLONE" && go test ./... -count=1 >"$WORK/mirror-test.log" 2>&1 ); then
    echo "  tests PASS: OK"
  else
    echo "REFUSING: the filtered mirror FAILS its own tests. Publishing would hand" >&2
    echo "          contributors a red suite. This usually means a publish transform" >&2
    echo "          (redaction, path strip) changed behaviour, not just text." >&2
    grep -E '^(--- FAIL|FAIL|\s+.*_test\.go:)' "$WORK/mirror-test.log" | head -20 >&2
    exit 1
  fi
else
  echo "  (go not found; skipping build check)" >&2
fi

# Authoritative secret scan: the single-branch clone IS the publish payload.
echo "Checking the published module path resolves to this repo ..."
mirror_assert_module_path "$CLONE" "$REMOTE_URL"

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


fi
# =========================== end PHASE: gate =================================

# In --gate-only mode this reports the clean result and exits. In every other
# mode it is a no-op.
mirror_phase_gates_done

# ============================= PHASE: push ===================================
# Holds the deploy key and the workflow token. Executes no repository code, does
# not mount the bare repo, and runs no gate: everything it publishes was built
# by the prepare phase and cleared by the gate phase.
# =============================================================================
if mirror_phase_does_push; then

if [ "$PUSH" -eq 0 ] && [ "$MIRROR_PHASE" = "all" ]; then
  echo; echo "DRY RUN. Filtered mirror ready at: $CLONE"
  echo "Would push its HEAD -> $REMOTE_URL main"
  echo "Re-run with --push once the public repo exists (gh repo create bright-interaction/flare --public)."
  trap - EXIT  # keep $WORK so the operator can inspect the dry-run tree
  exit 0
fi

echo "Pushing filtered mirror -> $REMOTE_URL main ..."
# Force-with-lease, because mirror_rewrite_authors and the redaction passes rewrite
# EVERY commit id, so the mirror can never fast-forward over what is published.
# The lease pins the overwrite to the head we actually observed, so a commit
# landed directly on the public repo aborts the push instead of vanishing.
# shellcheck source=../../scripts/mirror-push.sh
. "$ROOT/scripts/mirror-push.sh"
mirror_force_publish "$CLONE" "$REMOTE_URL"

# Cut the release tag when this product's VERSION file names one that is not
# published yet. The push above moves main; a tag is a SEPARATE ref and was
# never pushed at all, which is how reactor shipped every 2026-07-27 audit fix
# to main while `@latest` still resolved to the vulnerable v0.1.0, and how
# trustissues repeated it a day later with vault_entries.name cleartext at its
# only tag. Non-fatal by construction: main is already published by this line,
# so a tag problem warns and never fails the publish.
# shellcheck source=../../scripts/mirror-release-tag.sh
. "$ROOT/scripts/mirror-release-tag.sh"
mirror_release_tag "$CLONE" "$REMOTE_URL"

fi
# =========================== end PHASE: push =================================


echo "Done. Cleanup: git branch -D $SPLIT_BRANCH"
