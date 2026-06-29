# Flare licensing

Flare is open-core, mirroring Mesh.

## Core: AGPL-3.0

Everything in this `flare/` repo is licensed under the GNU AGPL-3.0 (see
[LICENSE](./LICENSE)): the ingest engine (errors, logs, traces), the storage
layer, the SvelteKit dashboard, the API-key + project provisioning surface, and
the self-hostable single binary. You can run the whole product yourself on one
Postgres.

The AGPL's network-use clause means if you offer Flare as a hosted service to
others, you must make your modified source available to those users.

## Commercial overlay (not in this repo)

The hosted, multi-tenant Flare service and the Cloud (Dockyard) auto-wiring
that provisions a project + injects a DSN per deploy are a separate commercial
overlay, not covered by the AGPL grant here.

## Commercial license

If the AGPL does not fit your use (for example, embedding Flare in a closed-source
product), a commercial license is available. Contact Bright Interaction.
