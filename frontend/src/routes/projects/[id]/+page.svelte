<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime, levelColor } from '$lib/format';
  import type { AlertRule, Issue, Project } from '$lib/types';

  const id = $derived(page.params.id ?? '');

  let project = $state<Project | null>(null);
  let issues = $state<Issue[] | null>(null);
  let total = $state(0);
  let error = $state<string | null>(null);
  let status = $state('unresolved');
  let setupOpen = $state(false);

  let rules = $state<AlertRule[]>([]);
  let ruleName = $state('');

  const filters = ['unresolved', 'resolved', 'ignored', 'all'];

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(async () => {
    try {
      project = await api.project(id);
      rules = await api.alertRules(id);
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
      const r = await api.createAlertRule(id, ruleName);
      rules = [r, ...rules];
      ruleName = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create rule';
    }
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
          Fire when a new issue first appears. Delivery goes to channels you add under
          <a href="/settings" class="text-amber-400 hover:text-amber-300">Alerts</a>.
        </p>
        <form onsubmit={addRule} class="mt-3 flex gap-2">
          <input
            bind:value={ruleName}
            placeholder="Notify on new issues"
            class="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-sm outline-none focus:border-amber-400/60"
          />
          <button class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm text-zinc-200 transition-colors hover:bg-zinc-800 active:translate-y-px">Add</button>
        </form>
        <ul class="mt-3 space-y-1.5">
          {#each rules as r (r.id)}
            <li class="flex items-center gap-2 text-sm text-zinc-300">
              <span class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></span>
              {r.name}
              <span class="font-mono text-xs text-zinc-500">{r.type}</span>
            </li>
          {:else}
            <li class="text-xs text-zinc-500">No rules yet.</li>
          {/each}
        </ul>
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
              <div class="truncate text-sm font-medium text-zinc-100">{issue.title}</div>
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
