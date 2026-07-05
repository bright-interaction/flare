<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime, levelColor } from '$lib/format';
  import type { AlertRule, Artifact, Issue, Project } from '$lib/types';

  const id = $derived(page.params.id ?? '');

  let project = $state<Project | null>(null);
  let issues = $state<Issue[] | null>(null);
  let total = $state(0);
  let error = $state<string | null>(null);
  let status = $state('unresolved');
  let setupOpen = $state(false);

  let rules = $state<AlertRule[]>([]);
  let ruleName = $state('');
  let hasChannel = $state(true); // assume true until loaded, so no flash
  let ruleType = $state('new_issue');
  let ruleThreshold = $state(10);
  let ruleWindow = $state(5);
  let confirmName = $state('');
  let deleting = $state(false);

  let artifacts = $state<Artifact[]>([]);
  let smRelease = $state('');
  let smName = $state('');
  let smFile = $state<File | null>(null);
  let smBusy = $state(false);

  const filters = ['unresolved', 'resolved', 'ignored', 'all'];

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(async () => {
    try {
      project = await api.project(id);
      rules = await api.alertRules(id);
      artifacts = await api.artifacts(id);
      hasChannel = (await api.channels()).some((c) => c.enabled);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load project';
    }
    loadIssues();
  });

  async function loadIssues() {
    issues = null;
    try {
      const res = await api.issues(id, status === 'all' ? undefined : status);
      issues = res.issues;
      total = res.total;
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load issues';
      issues = [];
    }
  }

  function setStatus(s: string) {
    status = s;
    loadIssues();
  }

  async function addRule(e: SubmitEvent) {
    e.preventDefault();
    if (!ruleName.trim()) return;
    try {
      const usesWindow = ruleType === 'spike' || ruleType === 'anomaly' || ruleType === 'silence';
      const r = await api.createAlertRule(id, {
        name: ruleName,
        type: ruleType,
        threshold: usesWindow ? ruleThreshold : 0,
        window_minutes: usesWindow ? ruleWindow : 0
      });
      rules = [r, ...rules];
      ruleName = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create rule';
    }
  }

  function applyRuleDefaults() {
    if (ruleType === 'spike') {
      ruleThreshold = 10;
      ruleWindow = 5;
    } else if (ruleType === 'anomaly') {
      ruleThreshold = 3;
      ruleWindow = 60;
    } else if (ruleType === 'silence') {
      ruleThreshold = 20;
      ruleWindow = 30;
    }
  }

  function ruleSummary(r: AlertRule) {
    if (r.type === 'spike') return `spike: ${r.threshold} events / ${r.window_minutes}m`;
    if (r.type === 'anomaly') return `anomaly: ${r.threshold}x baseline over ${r.window_minutes}m`;
    if (r.type === 'silence') return `silence: quiet ${r.window_minutes}m (normally >${r.threshold}/24h)`;
    if (r.type === 'regression') return 'regression';
    return 'new issue';
  }

  async function removeRule(ruleId: string) {
    try {
      await api.deleteAlertRule(id, ruleId);
      rules = rules.filter((r) => r.id !== ruleId);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to remove rule';
    }
  }

  async function deleteProject() {
    if (!project || confirmName !== project.name) return;
    deleting = true;
    error = null;
    try {
      await api.deleteProject(id);
      goto('/projects');
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to delete project';
      deleting = false;
    }
  }

  function onSmFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0] ?? null;
    smFile = f;
    if (f && !smName) smName = f.name.replace(/\.map$/, '');
  }

  async function uploadSourceMap(e: SubmitEvent) {
    e.preventDefault();
    if (!smFile || !smRelease.trim() || !smName.trim()) return;
    smBusy = true;
    error = null;
    try {
      const content = await smFile.text();
      const a = await api.uploadSourceMap(id, smRelease.trim(), smName.trim(), content);
      artifacts = [a, ...artifacts.filter((x) => x.id !== a.id)];
      smFile = null;
      smName = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to upload source map';
    } finally {
      smBusy = false;
    }
  }

  async function removeArtifact(artifactId: string) {
    try {
      await api.deleteArtifact(id, artifactId);
      artifacts = artifacts.filter((a) => a.id !== artifactId);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to remove source map';
    }
  }

  function fmtSize(n: number) {
    return n > 1024 * 1024 ? `${(n / 1024 / 1024).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`;
  }

  const snippet = $derived(
    project ? `Sentry.init({\n  dsn: "${project.dsn}"\n});` : ''
  );
</script>

{#if project}
  <div class="mb-6 flex items-center gap-2 text-sm text-zinc-500">
    <a href="/projects" class="hover:text-zinc-300">Projects</a>
    <span>/</span>
    <span class="text-zinc-300">{project.name}</span>
  </div>

  <div class="mb-6 flex items-end justify-between">
    <h1 class="text-xl font-semibold tracking-tight">{project.name}</h1>
    <button
      onclick={() => (setupOpen = !setupOpen)}
      class="rounded-md border border-zinc-800 px-3 py-1.5 text-sm text-zinc-400 transition-colors hover:border-zinc-700 hover:text-zinc-200"
    >
      {setupOpen ? 'Hide setup' : 'Setup & alerts'}
    </button>
  </div>

  <div class="mb-5 flex items-center gap-1 border-b border-zinc-800/80">
    <a href="/projects/{id}" class="-mb-px border-b-2 border-amber-400 px-3 py-2 text-sm text-zinc-100">Issues</a>
    <a href="/projects/{id}/logs" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Logs</a>
    <a href="/projects/{id}/traces" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Traces</a>
    <a href="/projects/{id}/releases" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Releases</a>
  </div>

  {#if setupOpen}
    <div class="mb-8 grid gap-6 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-5 md:grid-cols-2">
      <div>
        <h3 class="text-sm font-medium">Send events</h3>
        <p class="mt-1 text-xs text-zinc-500">
          Any Sentry SDK works. Set the DSN and errors flow in. OTLP endpoint:
          <code class="font-mono text-zinc-400">{project.otlp_endpoint}</code>
        </p>
        <pre class="mt-3 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed text-zinc-300">{snippet}</pre>
      </div>
      <div>
        <h3 class="text-sm font-medium">Alert rules</h3>
        <p class="mt-1 text-xs text-zinc-500">
          New issue, regression, spike (N events in M minutes), anomaly (error rate jumps vs the project's own
          baseline), or silence (a normally-active project goes quiet). Delivery goes to channels you add under
          <a href="/settings" class="text-amber-400 hover:text-amber-300">Alerts</a>.
        </p>
        {#if !hasChannel}
          <p class="mt-2 rounded-md border border-amber-900/50 bg-amber-950/20 px-3 py-2 text-xs text-amber-300/90">
            No notification channel is configured, so rules will not deliver anything yet. Add one under
            <a href="/settings" class="font-medium underline hover:text-amber-200">Alerts</a>.
          </p>
        {/if}
        <form onsubmit={addRule} class="mt-3 space-y-2">
          <div class="flex gap-2">
            <input
              bind:value={ruleName}
              placeholder="Rule name"
              class="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-amber-400/60"
            />
            <select
              bind:value={ruleType}
              onchange={applyRuleDefaults}
              class="rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-sm outline-none focus:border-amber-400/60"
            >
              <option value="new_issue">New issue</option>
              <option value="regression">Regression</option>
              <option value="spike">Spike</option>
              <option value="anomaly">Anomaly</option>
              <option value="silence">Silence</option>
            </select>
          </div>
          {#if ruleType === 'spike'}
            <div class="flex items-center gap-2 text-xs text-zinc-500">
              <span>when</span>
              <input
                type="number"
                min="1"
                bind:value={ruleThreshold}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>events in</span>
              <input
                type="number"
                min="1"
                bind:value={ruleWindow}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>minutes</span>
            </div>
          {:else if ruleType === 'anomaly'}
            <div class="flex items-center gap-2 text-xs text-zinc-500">
              <span>when error rate exceeds</span>
              <input
                type="number"
                min="2"
                bind:value={ruleThreshold}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>x its baseline over</span>
              <input
                type="number"
                min="1"
                bind:value={ruleWindow}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>minutes</span>
            </div>
          {:else if ruleType === 'silence'}
            <div class="flex items-center gap-2 text-xs text-zinc-500">
              <span>when quiet for</span>
              <input
                type="number"
                min="1"
                bind:value={ruleWindow}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>min and normally &gt;</span>
              <input
                type="number"
                min="1"
                bind:value={ruleThreshold}
                class="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-sm text-zinc-200 outline-none focus:border-amber-400/60"
              />
              <span>events/24h</span>
            </div>
          {/if}
          <button class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm text-zinc-200 transition-colors hover:bg-zinc-800 active:translate-y-px">Add rule</button>
        </form>
        <ul class="mt-3 space-y-1.5">
          {#each rules as r (r.id)}
            <li class="flex items-center gap-2 text-sm text-zinc-300">
              <span class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></span>
              {r.name}
              <span class="font-mono text-xs text-zinc-500">{ruleSummary(r)}</span>
              <button
                onclick={() => removeRule(r.id)}
                title="Remove rule"
                class="ml-auto text-xs text-zinc-600 transition-colors hover:text-rose-400"
              >
                Remove
              </button>
            </li>
          {:else}
            <li class="text-xs text-zinc-500">No rules yet.</li>
          {/each}
        </ul>
      </div>
    </div>

    <div class="mb-8 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-5">
      <h3 class="text-sm font-medium">Source maps</h3>
      <p class="mt-1 max-w-2xl text-xs text-zinc-500">
        Upload a <code class="font-mono text-zinc-400">.map</code> for each minified file in a release. Stack traces on matching errors are then resolved back to your original source, with context. Match by file name (e.g. <code class="font-mono text-zinc-400">app.min.js</code>) and the release your SDK reports.
      </p>
      <form onsubmit={uploadSourceMap} class="mt-3 flex flex-wrap items-end gap-2">
        <div class="flex flex-col gap-1.5">
          <label for="smrel" class="text-xs font-medium text-zinc-400">Release</label>
          <input
            id="smrel"
            bind:value={smRelease}
            placeholder="1.4.2"
            class="w-32 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-amber-400/60"
          />
        </div>
        <div class="flex flex-col gap-1.5">
          <label for="smname" class="text-xs font-medium text-zinc-400">Minified file</label>
          <input
            id="smname"
            bind:value={smName}
            placeholder="app.min.js"
            class="w-40 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-amber-400/60"
          />
        </div>
        <div class="flex flex-col gap-1.5">
          <label for="smfile" class="text-xs font-medium text-zinc-400">.map file</label>
          <input
            id="smfile"
            type="file"
            accept=".map,.json,application/json"
            onchange={onSmFile}
            class="w-56 text-xs text-zinc-400 file:mr-2 file:rounded-md file:border-0 file:bg-zinc-800 file:px-2.5 file:py-1.5 file:text-xs file:text-zinc-200 hover:file:bg-zinc-700"
          />
        </div>
        <button
          disabled={smBusy || !smFile}
          class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm text-zinc-200 transition-colors hover:bg-zinc-800 active:translate-y-px disabled:opacity-40"
        >
          {smBusy ? 'Uploading...' : 'Upload'}
        </button>
      </form>
      {#if artifacts.length}
        <ul class="mt-4 divide-y divide-zinc-800/60 text-sm">
          {#each artifacts as a (a.id)}
            <li class="flex items-center gap-3 py-2">
              <span class="rounded bg-zinc-800/80 px-1.5 py-0.5 font-mono text-[11px] text-zinc-400">{a.release}</span>
              <span class="truncate font-mono text-[13px] text-zinc-200">{a.name}</span>
              <span class="text-xs text-zinc-600">{fmtSize(a.size)}</span>
              <button
                onclick={() => removeArtifact(a.id)}
                class="ml-auto shrink-0 text-xs text-zinc-600 transition-colors hover:text-rose-400"
              >
                Remove
              </button>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="mt-3 text-xs text-zinc-600">No source maps uploaded yet.</p>
      {/if}
    </div>

    <div class="mb-8 rounded-lg border border-rose-900/50 bg-rose-950/20 p-5">
      <h3 class="text-sm font-medium text-rose-300">Danger zone</h3>
      <p class="mt-1 max-w-2xl text-xs text-zinc-500">
        Deleting <span class="font-medium text-zinc-300">{project.name}</span> permanently removes the project, its DSN, and every issue, log, and trace it holds. This cannot be undone.
      </p>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <input
          bind:value={confirmName}
          placeholder="Type {project.name} to confirm"
          class="min-w-[14rem] flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-rose-500/60"
        />
        <button
          onclick={deleteProject}
          disabled={confirmName !== project.name || deleting}
          class="rounded-md border border-rose-700/70 bg-rose-600/10 px-3 py-1.5 text-sm text-rose-300 transition-colors hover:bg-rose-600/20 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-40"
        >
          {deleting ? 'Deleting...' : 'Delete project'}
        </button>
      </div>
    </div>
  {/if}

  <div class="mb-3 flex items-center gap-1 border-b border-zinc-800/80">
    {#each filters as f (f)}
      <button
        onclick={() => setStatus(f)}
        class="-mb-px border-b-2 px-3 py-2 text-sm capitalize transition-colors {status === f
          ? 'border-amber-400 text-zinc-100'
          : 'border-transparent text-zinc-500 hover:text-zinc-300'}"
      >
        {f}
      </button>
    {/each}
    <span class="ml-auto font-mono text-xs text-zinc-600">{total} total</span>
  </div>

  {#if issues === null}
    <div class="divide-y divide-zinc-800/60">
      {#each Array(4) as _, i (i)}
        <div class="h-14 animate-pulse bg-zinc-900/30"></div>
      {/each}
    </div>
  {:else if issues.length === 0}
    <div class="rounded-lg border border-dashed border-zinc-800 px-6 py-14 text-center">
      <p class="text-sm font-medium text-zinc-300">Nothing here</p>
      <p class="mx-auto mt-1 max-w-sm text-sm text-zinc-500">
        No {status === 'all' ? '' : status} issues. Send a test error with your DSN and it shows up here.
      </p>
    </div>
  {:else}
    <ul class="divide-y divide-zinc-800/60">
      {#each issues as issue (issue.id)}
        <li>
          <a href="/issues/{issue.id}" class="flex items-center gap-4 py-3 transition-colors hover:bg-zinc-900/40">
            <span class="inline-block h-2 w-2 shrink-0 rounded-full {issue.status === 'resolved' ? 'bg-emerald-400' : issue.status === 'ignored' ? 'bg-zinc-600' : 'bg-amber-400'}"></span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <div class="truncate text-sm font-medium text-zinc-100">{issue.title}</div>
                {#if issue.sensitive}
                  <span
                    class="shrink-0 rounded border border-rose-800/70 bg-rose-950/40 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-rose-300"
                    title="Payload may contain: {issue.sensitive}"
                  >
                    Sensitive
                  </span>
                {/if}
              </div>
              <div class="mt-0.5 flex items-center gap-2 text-xs text-zinc-500">
                <span class="uppercase {levelColor(issue.level)}">{issue.level}</span>
                {#if issue.culprit}<span class="truncate font-mono">{issue.culprit}</span>{/if}
              </div>
            </div>
            <div class="shrink-0 text-right">
              <div class="font-mono text-sm text-zinc-300">{issue.event_count}</div>
              <div class="text-xs text-zinc-600">{relativeTime(issue.last_seen)}</div>
            </div>
          </a>
        </li>
      {/each}
    </ul>
  {/if}
{:else if error}
  <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{:else}
  <div class="flex min-h-[30vh] items-center justify-center text-sm text-zinc-500">Loading...</div>
{/if}
