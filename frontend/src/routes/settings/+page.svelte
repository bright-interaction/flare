<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime } from '$lib/format';
  import type { AiConfig, ApiKey, AuditEntry, Channel, GithubConfig, OidcConfig } from '$lib/types';

  let channels = $state<Channel[] | null>(null);
  let error = $state<string | null>(null);
  let type = $state('log');
  let url = $state('');
  let emailTo = $state('');
  let slackUrl = $state('');
  let busy = $state(false);

  let apiKeys = $state<ApiKey[] | null>(null);
  let keyName = $state('');
  let newKey = $state<string | null>(null);
  let keyBusy = $state(false);

  let github = $state<GithubConfig | null>(null);
  let ghRepo = $state('');
  let ghToken = $state('');
  let ghBusy = $state(false);
  const isAdmin = $derived(session.user?.role === 'admin' || session.user?.role === 'owner');
  const isOwner = $derived(session.user?.role === 'owner');

  let audit = $state<AuditEntry[]>([]);
  let exporting = $state(false);
  let confirmDelete = $state('');
  let deletingOrg = $state(false);

  let ai = $state<AiConfig | null>(null);
  let aiBase = $state('');
  let aiKey = $state('');
  let aiModel = $state('');
  let aiFormat = $state('openai');
  let aiEnabled = $state(false);
  let aiBusy = $state(false);

  let oidc = $state<OidcConfig | null>(null);
  let ssoIssuer = $state('');
  let ssoClientId = $state('');
  let ssoSecret = $state('');
  let ssoRole = $state('member');
  let ssoEnabled = $state(false);
  let ssoBusy = $state(false);
  let copiedRedirect = $state(false);

  // AI operator (MCP): everything a tenant needs to connect an MCP client. The
  // endpoint is this deployment's own origin; the key is the one just created
  // (shown once) or a placeholder until the tenant makes one above.
  let mcpUrl = $state('');
  let copiedMcp = $state<string | null>(null);
  const mcpKey = $derived(newKey ?? '<YOUR_API_KEY>');
  const mcpCommand = $derived(
    `claude mcp add --transport http flare ${mcpUrl} --header "Authorization: Bearer ${mcpKey}"`
  );
  const mcpJsonConfig = $derived(
    JSON.stringify(
      { mcpServers: { flare: { type: 'http', url: mcpUrl, headers: { Authorization: `Bearer ${mcpKey}` } } } },
      null,
      2
    )
  );

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(load);

  async function load() {
    mcpUrl = `${location.origin}/api/mcp`;
    try {
      channels = await api.channels();
      apiKeys = await api.apiKeys();
      if (isAdmin) {
        github = await api.githubConfig();
        ghRepo = github.repo;
        audit = await api.auditLog();
        oidc = await api.oidcConfig();
        ssoIssuer = oidc.issuer;
        ssoClientId = oidc.client_id;
        ssoRole = oidc.default_role || 'member';
        ssoEnabled = oidc.enabled;
        ai = await api.aiConfig();
        aiBase = ai.base_url;
        aiModel = ai.model;
        aiFormat = ai.format || 'openai';
        aiEnabled = ai.enabled;
      }
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load settings';
    }
  }

  async function exportData() {
    exporting = true;
    error = null;
    try {
      const bundle = await api.exportData();
      const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'flare-export.json';
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Export failed';
    } finally {
      exporting = false;
    }
  }

  async function deleteOrg() {
    if (confirmDelete !== 'DELETE') return;
    deletingOrg = true;
    error = null;
    try {
      await api.deleteOrg();
      session.user = null;
      goto('/login', { replaceState: true });
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to delete workspace';
      deletingOrg = false;
    }
  }

  function fmtAction(a: string) {
    return a.replace(/\./g, ' ');
  }

  async function saveOidc(e: SubmitEvent) {
    e.preventDefault();
    ssoBusy = true;
    error = null;
    try {
      oidc = await api.setOidcConfig({
        issuer: ssoIssuer.trim(),
        client_id: ssoClientId.trim(),
        client_secret: ssoSecret.trim(), // blank keeps the stored secret (server-side)
        default_role: ssoRole,
        enabled: ssoEnabled
      });
      ssoSecret = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to save SSO config';
    } finally {
      ssoBusy = false;
    }
  }

  async function disconnectOidc() {
    if (!confirm('Disconnect SSO? Members will sign in with email + password again.')) return;
    try {
      await api.deleteOidcConfig();
      oidc = await api.oidcConfig();
      ssoIssuer = '';
      ssoClientId = '';
      ssoEnabled = false;
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to disconnect SSO';
    }
  }

  async function copyRedirect() {
    if (!oidc) return;
    await navigator.clipboard.writeText(oidc.redirect_uri);
    copiedRedirect = true;
    setTimeout(() => (copiedRedirect = false), 1500);
  }

  async function copyMcp(text: string, id: string) {
    await navigator.clipboard.writeText(text);
    copiedMcp = id;
    setTimeout(() => {
      if (copiedMcp === id) copiedMcp = null;
    }, 1500);
  }

  async function saveAi(e: SubmitEvent) {
    e.preventDefault();
    aiBusy = true;
    error = null;
    try {
      ai = await api.setAiConfig({
        base_url: aiBase.trim(),
        api_key: aiKey.trim(),
        model: aiModel.trim(),
        format: aiFormat,
        enabled: aiEnabled
      });
      aiKey = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to save AI config';
    } finally {
      aiBusy = false;
    }
  }

  async function disconnectAi() {
    if (!confirm('Disconnect AI triage?')) return;
    try {
      await api.deleteAiConfig();
      ai = await api.aiConfig();
      aiBase = '';
      aiModel = '';
      aiEnabled = false;
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to disconnect AI';
    }
  }

  async function saveGithub(e: SubmitEvent) {
    e.preventDefault();
    if (!ghRepo.trim() || !ghToken.trim()) return;
    ghBusy = true;
    error = null;
    try {
      github = await api.setGithubConfig(ghRepo.trim(), ghToken.trim());
      ghToken = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to save GitHub config';
    } finally {
      ghBusy = false;
    }
  }

  async function clearGithub() {
    if (!confirm('Disconnect GitHub?')) return;
    try {
      await api.deleteGithubConfig();
      github = { configured: false, repo: '' };
      ghRepo = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to disconnect GitHub';
    }
  }

  async function createKey(e: SubmitEvent) {
    e.preventDefault();
    keyBusy = true;
    error = null;
    try {
      const k = await api.createApiKey(keyName.trim() || 'API key');
      newKey = k.key;
      apiKeys = [
        { id: k.id, name: k.name, prefix: k.prefix, created_at: new Date().toISOString(), last_used_at: null },
        ...(apiKeys ?? [])
      ];
      keyName = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create key';
    } finally {
      keyBusy = false;
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    try {
      const config =
        type === 'webhook'
          ? { url }
          : type === 'email'
            ? { to: emailTo }
            : type === 'slack'
              ? { webhook_url: slackUrl }
              : {};
      const ch = await api.createChannel(type, config);
      channels = [ch, ...(channels ?? [])];
      url = '';
      emailTo = '';
      slackUrl = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create channel';
    } finally {
      busy = false;
    }
  }

  async function removeChannel(chId: string) {
    if (!confirm('Remove this alert channel? Rules pointing at it will stop delivering.')) return;
    try {
      await api.deleteChannel(chId);
      channels = (channels ?? []).filter((c) => c.id !== chId);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to remove channel';
    }
  }

  async function revokeKey(keyId: string) {
    if (!confirm('Revoke this API key? Anything using it stops working immediately.')) return;
    try {
      await api.deleteApiKey(keyId);
      apiKeys = (apiKeys ?? []).filter((k) => k.id !== keyId);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to revoke key';
    }
  }
</script>

<h1 class="text-xl font-semibold tracking-tight">Alert channels</h1>
<p class="mt-1 mb-8 text-sm text-zinc-500">
  Where new-issue alerts are delivered. Attach rules to a project from its Setup panel.
</p>

<form onsubmit={add} class="mb-8 flex flex-wrap items-end gap-3 border-b border-zinc-800/80 pb-8">
  <div class="flex flex-col gap-1.5">
    <label for="type" class="text-xs font-medium text-zinc-400">Channel type</label>
    <select
      id="type"
      bind:value={type}
      class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
    >
      <option value="log">Server log</option>
      <option value="email">Email</option>
      <option value="slack">Slack</option>
      <option value="webhook">Webhook</option>
    </select>
  </div>
  {#if type === 'slack'}
    <div class="flex flex-1 flex-col gap-1.5">
      <label for="slackurl" class="text-xs font-medium text-zinc-400">Slack incoming webhook URL</label>
      <input
        id="slackurl"
        bind:value={slackUrl}
        placeholder="https://hooks.slack.com/services/..."
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
      <span class="text-xs text-zinc-600">Create one under Slack apps &rarr; Incoming Webhooks.</span>
    </div>
  {:else if type === 'webhook'}
    <div class="flex flex-1 flex-col gap-1.5">
      <label for="url" class="text-xs font-medium text-zinc-400">Webhook URL</label>
      <input
        id="url"
        bind:value={url}
        placeholder="https://hooks.example.com/flare"
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
      <span class="text-xs text-zinc-600">Private and loopback addresses are rejected.</span>
    </div>
  {:else if type === 'email'}
    <div class="flex flex-1 flex-col gap-1.5">
      <label for="emailto" class="text-xs font-medium text-zinc-400">Send to</label>
      <input
        id="emailto"
        type="email"
        bind:value={emailTo}
        placeholder="alerts@yourteam.com"
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
      <span class="text-xs text-zinc-600">New-issue alerts are emailed here.</span>
    </div>
  {/if}
  <button
    type="submit"
    disabled={busy}
    class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
  >
    {busy ? 'Adding...' : 'Add channel'}
  </button>
</form>

{#if error}
  <p class="mb-6 rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{/if}

{#if channels === null}
  <div class="space-y-2">
    {#each Array(2) as _, i (i)}
      <div class="h-12 animate-pulse rounded-md border border-zinc-800/60 bg-zinc-900/40"></div>
    {/each}
  </div>
{:else if channels.length === 0}
  <div class="rounded-lg border border-dashed border-zinc-800 px-6 py-12 text-center">
    <p class="text-sm font-medium text-zinc-300">No channels yet</p>
    <p class="mt-1 text-sm text-zinc-500">Add one above. "Server log" is the quickest way to verify alerts fire.</p>
  </div>
{:else}
  <ul class="divide-y divide-zinc-800/60">
    {#each channels as ch (ch.id)}
      <li class="flex items-center gap-3 py-3">
        <span class="inline-block h-1.5 w-1.5 rounded-full {ch.enabled ? 'bg-emerald-400' : 'bg-zinc-600'}"></span>
        <span class="text-sm font-medium capitalize text-zinc-200">{ch.type}</span>
        {#if ch.config?.url}<span class="truncate font-mono text-xs text-zinc-500">{String(ch.config.url)}</span>{/if}
        {#if ch.config?.to}<span class="truncate font-mono text-xs text-zinc-500">{String(ch.config.to)}</span>{/if}
        {#if ch.config?.webhook_url}<span class="truncate font-mono text-xs text-zinc-500">{String(ch.config.webhook_url)}</span>{/if}
        <button
          onclick={() => removeChannel(ch.id)}
          class="ml-auto shrink-0 text-xs text-zinc-600 transition-colors hover:text-rose-400"
        >
          Remove
        </button>
      </li>
    {/each}
  </ul>
{/if}

<h2 class="mt-14 text-xl font-semibold tracking-tight">API keys</h2>
<p class="mt-1 mb-8 text-sm text-zinc-500">
  Org-scoped keys for programmatic access (Bearer auth). Used by Cloud to auto-provision a
  project per service, and for OTLP ingest via the <code class="font-mono">x-flare-key</code> header.
</p>

<form onsubmit={createKey} class="mb-6 flex flex-wrap items-end gap-3 border-b border-zinc-800/80 pb-8">
  <div class="flex flex-1 flex-col gap-1.5">
    <label for="keyname" class="text-xs font-medium text-zinc-400">Key name</label>
    <input
      id="keyname"
      bind:value={keyName}
      placeholder="gopile"
      class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
    />
  </div>
  <button
    type="submit"
    disabled={keyBusy}
    class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
  >
    {keyBusy ? 'Creating...' : 'Create key'}
  </button>
</form>

{#if newKey}
  <div class="mb-6 rounded-md border border-amber-400/40 bg-amber-400/10 px-4 py-3">
    <p class="text-sm font-medium text-amber-200">Copy this key now. It will not be shown again.</p>
    <code class="mt-2 block break-all font-mono text-xs text-amber-100">{newKey}</code>
  </div>
{/if}

{#if apiKeys && apiKeys.length}
  <ul class="divide-y divide-zinc-800/60">
    {#each apiKeys as k (k.id)}
      <li class="flex items-center gap-3 py-3">
        <span class="text-sm font-medium text-zinc-200">{k.name}</span>
        <span class="font-mono text-xs text-zinc-500">{k.prefix}...</span>
        <span class="ml-auto text-xs text-zinc-600">
          {k.last_used_at ? 'used' : 'never used'}
        </span>
        <button
          onclick={() => revokeKey(k.id)}
          class="shrink-0 text-xs text-zinc-600 transition-colors hover:text-rose-400"
        >
          Revoke
        </button>
      </li>
    {/each}
  </ul>
{:else if apiKeys}
  <p class="text-sm text-zinc-500">No keys yet.</p>
{/if}

<h2 class="mt-14 text-xl font-semibold tracking-tight">AI operator (MCP)</h2>
<p class="mt-1 mb-6 max-w-2xl text-sm text-zinc-500">
  Flare speaks the
  <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener" class="text-amber-400 transition-colors hover:text-amber-300">Model Context Protocol</a>,
  so an AI client (Claude Code, Cursor, and others) can investigate and act on your errors directly:
  pull an overview, read a stack trace, search logs, run AI triage, and resolve issues. Reads work for any
  member; the two write tools (resolve and triage) need a member key. It authenticates with an org API key,
  created above, and stays scoped to this workspace.
</p>

<div class="mb-4">
  <div class="mb-1.5 text-xs font-medium text-zinc-400">Endpoint</div>
  <div class="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-900/60 px-3 py-2">
    <code class="flex-1 truncate font-mono text-[12px] text-zinc-300">{mcpUrl}</code>
    <button
      onclick={() => copyMcp(mcpUrl, 'url')}
      class="shrink-0 rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-400 transition-colors hover:border-zinc-700 hover:text-zinc-200"
    >
      {copiedMcp === 'url' ? 'Copied' : 'Copy'}
    </button>
  </div>
</div>

<div class="mb-4">
  <div class="mb-1.5 flex items-center gap-2">
    <span class="text-xs font-medium text-zinc-400">Connect Claude Code</span>
    <button
      onclick={() => copyMcp(mcpCommand, 'cmd')}
      class="rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-400 transition-colors hover:border-zinc-700 hover:text-zinc-200"
    >
      {copiedMcp === 'cmd' ? 'Copied' : 'Copy'}
    </button>
  </div>
  <pre class="overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2.5 font-mono text-[12px] leading-relaxed text-zinc-300">{mcpCommand}</pre>
  {#if newKey}
    <p class="mt-1.5 text-sm text-amber-200/80">Your new key is filled in above. Run this in your terminal, then restart the client. The key is shown only once.</p>
  {:else}
    <p class="mt-1.5 text-xs text-zinc-600">Create a key above and it drops into this command automatically. Run it in your terminal, then restart the client.</p>
  {/if}
</div>

<details class="mb-2">
  <summary class="cursor-pointer text-xs text-zinc-500 transition-colors hover:text-zinc-300">Other MCP clients (raw config)</summary>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2.5 font-mono text-[12px] leading-relaxed text-zinc-400">{mcpJsonConfig}</pre>
</details>

{#if isAdmin}
  <h2 class="mt-14 text-xl font-semibold tracking-tight">GitHub</h2>
  <p class="mt-1 mb-6 text-sm text-zinc-500">
    Connect a repo to open a GitHub issue straight from any Flare issue. The token is stored for this
    workspace and never shown again. A fine-grained token with <code class="font-mono">Issues: write</code> is enough.
  </p>

  {#if github?.configured}
    <div class="mb-4 flex items-center gap-3 rounded-md border border-zinc-800/80 bg-zinc-900/40 px-4 py-3">
      <span class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></span>
      <span class="text-sm text-zinc-200">Connected to <span class="font-mono">{github.repo}</span></span>
      <button onclick={clearGithub} class="ml-auto text-xs text-zinc-600 transition-colors hover:text-rose-400">Disconnect</button>
    </div>
  {/if}

  <form onsubmit={saveGithub} class="flex flex-wrap items-end gap-3">
    <div class="flex flex-col gap-1.5">
      <label for="ghrepo" class="text-xs font-medium text-zinc-400">Repository</label>
      <input
        id="ghrepo"
        bind:value={ghRepo}
        placeholder="owner/repo"
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
    </div>
    <div class="flex flex-1 flex-col gap-1.5">
      <label for="ghtoken" class="text-xs font-medium text-zinc-400">{github?.configured ? 'Replace token' : 'Token'}</label>
      <input
        id="ghtoken"
        type="password"
        bind:value={ghToken}
        placeholder="ghp_..."
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
    </div>
    <button
      type="submit"
      disabled={ghBusy}
      class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
    >
      {ghBusy ? 'Saving...' : github?.configured ? 'Update' : 'Connect'}
    </button>
  </form>

  <h2 class="mt-14 text-xl font-semibold tracking-tight">Single sign-on (OIDC)</h2>
  <p class="mt-1 mb-4 text-sm text-zinc-500">
    Let members sign in through your identity provider (Zitadel, Okta, Auth0, Keycloak, Entra). Register an
    OIDC app there with the redirect URI below, then paste its client id + secret.
  </p>

  {#if oidc}
    <div class="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-zinc-800/80 bg-zinc-900/40 px-4 py-3 text-sm">
      <span class="text-zinc-400">Redirect URI</span>
      <code class="truncate font-mono text-[12px] text-zinc-300">{oidc.redirect_uri}</code>
      <button onclick={copyRedirect} class="rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-400 hover:border-zinc-700 hover:text-zinc-200">
        {copiedRedirect ? 'Copied' : 'Copy'}
      </button>
      {#if oidc.enabled}
        <span class="ml-auto inline-flex items-center gap-1.5 text-xs text-emerald-400"><span class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></span>enabled</span>
      {/if}
    </div>
  {/if}

  <form onsubmit={saveOidc} class="space-y-3">
    <div class="flex flex-wrap gap-3">
      <div class="flex flex-1 flex-col gap-1.5">
        <label for="ssoissuer" class="text-xs font-medium text-zinc-400">Issuer URL</label>
        <input id="ssoissuer" bind:value={ssoIssuer} placeholder="https://auth.example.com" class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
      <div class="flex flex-col gap-1.5">
        <label for="ssorole" class="text-xs font-medium text-zinc-400">Default role for new members</label>
        <select id="ssorole" bind:value={ssoRole} class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm capitalize outline-none focus:border-amber-400/60">
          <option value="viewer">viewer</option>
          <option value="member">member</option>
          <option value="admin">admin</option>
        </select>
      </div>
    </div>
    <div class="flex flex-wrap gap-3">
      <div class="flex flex-1 flex-col gap-1.5">
        <label for="ssocid" class="text-xs font-medium text-zinc-400">Client ID</label>
        <input id="ssocid" bind:value={ssoClientId} placeholder="client id" class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
      <div class="flex flex-1 flex-col gap-1.5">
        <label for="ssosecret" class="text-xs font-medium text-zinc-400">{oidc?.issuer ? 'Replace client secret' : 'Client secret'}</label>
        <input id="ssosecret" type="password" bind:value={ssoSecret} placeholder={oidc?.issuer ? 'leave blank to keep' : 'client secret'} class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm text-zinc-300">
      <input type="checkbox" bind:checked={ssoEnabled} class="h-4 w-4 rounded border-zinc-700 bg-zinc-900 accent-amber-400" />
      Enable SSO sign-in for this workspace
    </label>
    <div class="flex items-center gap-3">
      <button type="submit" disabled={ssoBusy} class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60">
        {ssoBusy ? 'Saving...' : 'Save SSO'}
      </button>
      {#if oidc?.issuer}
        <button type="button" onclick={disconnectOidc} class="text-xs text-zinc-600 transition-colors hover:text-rose-400">Disconnect</button>
      {/if}
    </div>
  </form>

  <h2 class="mt-14 text-xl font-semibold tracking-tight">AI triage</h2>
  <p class="mt-1 mb-4 text-sm text-zinc-500">
    Point Flare at your own model endpoint (OpenAI-compatible or Anthropic Messages - OpenAI, a self-hosted
    vLLM/Ollama, an EU provider). Triage runs on <strong>your</strong> endpoint and PII is scrubbed from the issue
    before it is sent - so personal data never leaves your boundary.
  </p>

  {#if ai?.enabled}
    <div class="mb-4 flex items-center gap-3 rounded-md border border-zinc-800/80 bg-zinc-900/40 px-4 py-3">
      <span class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></span>
      <span class="text-sm text-zinc-200"><span class="font-mono">{ai.model}</span> via <span class="font-mono">{ai.base_url}</span></span>
      <button onclick={disconnectAi} class="ml-auto text-xs text-zinc-600 transition-colors hover:text-rose-400">Disconnect</button>
    </div>
  {/if}

  <form onsubmit={saveAi} class="space-y-3">
    <div class="flex flex-wrap gap-3">
      <div class="flex flex-1 flex-col gap-1.5">
        <label for="aibase" class="text-xs font-medium text-zinc-400">Endpoint base URL</label>
        <input id="aibase" bind:value={aiBase} placeholder="https://api.openai.com/v1" class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
      <div class="flex flex-col gap-1.5">
        <label for="aifmt" class="text-xs font-medium text-zinc-400">API format</label>
        <select id="aifmt" bind:value={aiFormat} class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60">
          <option value="openai">OpenAI-compatible</option>
          <option value="anthropic">Anthropic Messages</option>
        </select>
      </div>
    </div>
    <div class="flex flex-wrap gap-3">
      <div class="flex flex-col gap-1.5">
        <label for="aimodel" class="text-xs font-medium text-zinc-400">Model</label>
        <input id="aimodel" bind:value={aiModel} placeholder="gpt-4o-mini" class="w-48 rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
      <div class="flex flex-1 flex-col gap-1.5">
        <label for="aikey" class="text-xs font-medium text-zinc-400">{ai?.base_url ? 'Replace API key' : 'API key'}</label>
        <input id="aikey" type="password" bind:value={aiKey} placeholder={ai?.base_url ? 'leave blank to keep' : 'sk-...'} class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60" />
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm text-zinc-300">
      <input type="checkbox" bind:checked={aiEnabled} class="h-4 w-4 rounded border-zinc-700 bg-zinc-900 accent-amber-400" />
      Enable AI triage on issues
    </label>
    <div class="flex items-center gap-3">
      <button type="submit" disabled={aiBusy} class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60">
        {aiBusy ? 'Saving...' : 'Save AI'}
      </button>
      {#if ai?.base_url}
        <button type="button" onclick={disconnectAi} class="text-xs text-zinc-600 transition-colors hover:text-rose-400">Disconnect</button>
      {/if}
    </div>
  </form>

  <h2 class="mt-14 text-xl font-semibold tracking-tight">Audit log</h2>
  <p class="mt-1 mb-6 text-sm text-zinc-500">Sensitive actions across the workspace, newest first.</p>
  {#if audit.length}
    <ul class="divide-y divide-zinc-800/60 text-sm">
      {#each audit as a (a.created_at + a.action + a.target)}
        <li class="flex items-center gap-3 py-2.5">
          <span class="font-mono text-xs capitalize text-zinc-200">{fmtAction(a.action)}</span>
          {#if a.target}<span class="truncate text-zinc-400">{a.target}</span>{/if}
          <span class="ml-auto shrink-0 text-xs text-zinc-600">{a.actor}</span>
          <span class="shrink-0 text-xs text-zinc-600">{relativeTime(a.created_at)}</span>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="text-sm text-zinc-500">No audited actions yet.</p>
  {/if}

  <h2 class="mt-14 text-xl font-semibold tracking-tight">Data</h2>
  <p class="mt-1 mb-4 text-sm text-zinc-500">
    Download the workspace's structured data (members, projects, issues, alert rules, channels, releases) as JSON.
  </p>
  <button
    onclick={exportData}
    disabled={exporting}
    class="rounded-md border border-zinc-700 px-3.5 py-2 text-sm text-zinc-200 transition-colors hover:bg-zinc-800 active:translate-y-px disabled:opacity-60"
  >
    {exporting ? 'Preparing...' : 'Export workspace data'}
  </button>

  {#if isOwner}
    <div class="mt-14 rounded-lg border border-rose-900/50 bg-rose-950/20 p-5">
      <h3 class="text-sm font-medium text-rose-300">Delete workspace</h3>
      <p class="mt-1 max-w-2xl text-xs text-zinc-500">
        Permanently erase this workspace and everything in it - every project, issue, log, trace, member, key and setting. This cannot be undone.
      </p>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <input
          bind:value={confirmDelete}
          placeholder="Type DELETE to confirm"
          class="min-w-[14rem] flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-rose-500/60"
        />
        <button
          onclick={deleteOrg}
          disabled={confirmDelete !== 'DELETE' || deletingOrg}
          class="rounded-md border border-rose-700/70 bg-rose-600/10 px-3 py-1.5 text-sm text-rose-300 transition-colors hover:bg-rose-600/20 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-40"
        >
          {deletingOrg ? 'Deleting...' : 'Delete workspace'}
        </button>
      </div>
    </div>
  {/if}
{/if}
