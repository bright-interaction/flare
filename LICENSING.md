# Flare licensing

Flare is open core (fair-code).

## Core: Flare Sustainable Use License

Everything in this repo is licensed under the Flare Sustainable Use License (see
[LICENSE](./LICENSE)): the ingest engine (errors, logs, traces), the storage
layer, the SvelteKit dashboard, the API-key + project provisioning surface, and
the self-hostable single binary. You can run the whole product yourself on one
Postgres, for free, forever.

This is a [fair-code](https://faircode.io) license, not an OSI "open source"
license. The one limit: you may not resell Flare or run it as a hosted service
for third parties (a competing "Flare cloud"). Self-hosting, internal commercial
use, and operating monitoring for your own clients are all fine.

## Enterprise (not in this repo)

The hosted, multi-tenant Flare service and the fleet auto-wiring that provisions
a project and injects a DSN per deploy across many services are a separate
commercial enterprise product, held back from this repository.

## Commercial license

If you want to do something the Sustainable Use License does not permit (for
example, offering Flare as a hosted service to third parties), a commercial
license is available at licensing@brightinteraction.com.
