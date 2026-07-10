# Releasing Flare as a public `go install` tool

Flare's module path is `github.com/bright-interaction/flare`, but the code lives in
the private `bright-interaction/automations` monorepo under `flare/`. Go resolves a
module from a repo whose path matches the module path, so a public release is:
**mirror the `flare/` subtree to its own repo `github.com/bright-interaction/flare`,
then tag a version.**

Flare is open core (see [../LICENSING.md](../LICENSING.md)): the mirror carries the
whole `flare/` tree under the Flare Sustainable Use License. The enterprise product (hosted multi-tenant
control plane) lives outside this repo, so nothing is stripped for licensing. The
split script strips only the estate deploy compose and redacts internal infra
hostnames from history (`scripts/split-public-repo.sh`).

This is an outward, hard-to-reverse step (it exposes the source publicly), so it is
a deliberate operator action, not part of `git psync`. Run it once, then each later
release is a re-run plus a new tag. It requires `git-filter-repo` and `gitleaks` on
PATH.

## One-time: create the public repo and seed it

1. Dry run first (safe, no push): `./scripts/split-public-repo.sh`. It subtree-splits,
   strips/redacts, build-checks and gitleaks-scans the filtered tree, then prints what
   it WOULD push. Get this green before step 2.
2. Create the public repo (outward):
   ```
   gh repo create bright-interaction/flare --public \
     --description "Sovereign self-hostable observability (errors, logs, traces) on one Postgres. Fair-code."
   ```
3. Mirror and push:
   ```
   ./scripts/split-public-repo.sh --push
   ```
4. Verify the install resolves before tagging:
   ```
   go install github.com/bright-interaction/flare/cmd/server@latest
   ```

## Cut a version

Tags live on the PUBLIC repo (a monorepo tag cannot satisfy a differing module path):

```
# in a clone of github.com/bright-interaction/flare:
git tag v0.1.0
git push origin v0.1.0
```

Suggested first tag: **v0.1.0** (the HTTP + MCP surface is stable in shape, not yet frozen).

## Each subsequent release

```
./scripts/split-public-repo.sh --push      # re-mirror the latest flare/ subtree
# then tag the new version on the public repo
```

## What stays private

The hosted control plane, ops scripts, `.env`s and the rest of the monorepo never
leave `bright-interaction/automations`. Only the `flare/` subtree is mirrored, minus
the estate `docker-compose.yml`. Double-check no secret was ever committed under
`flare/` before the first public push (the split script's gitleaks gate enforces this).
