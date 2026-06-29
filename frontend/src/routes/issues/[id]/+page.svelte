<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime, levelColor, statusColor } from '$lib/format';
  import type { Issue, IssueEvent } from '$lib/types';

  const id = $derived(page.params.id ?? '');

  let issue = $state<Issue | null>(null);
  let events = $state<IssueEvent[]>([]);
  let error = $state<string | null>(null);

  const latest = $derived(events[0] ?? null);

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(async () => {
    try {
      issue = await api.issue(id);
      events = await api.issueEvents(id);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load issue';
    }
  });

  async function setStatus(s: string) {
    try {
      issue = await api.setIssueStatus(id, s);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to update status';
    }
  }
</script>

{#if issue}
  <div class="mb-6 flex items-center gap-2 text-sm text-zinc-500">
    <a href="/projects" class="hover:text-zinc-300">Projects</a>
    <span>/</span>
    <span class="text-zinc-300">Issue</span>
  </div>

  <div class="flex flex-wrap items-start justify-between gap-4">
    <div class="min-w-0">
      <div class="flex items-center gap-2.5">
        <span class="text-xs font-semibold uppercase {levelColor(issue.level)}">{issue.level}</span>
        <span class="rounded-full border px-2 py-0.5 text-xs capitalize {statusColor(issue.status)}">{issue.status}</span>
      </div>
      <h1 class="mt-2 text-lg font-semibold tracking-tight break-words">{issue.title}</h1>
      {#if issue.culprit}<p class="mt-1 font-mono text-sm text-zinc-500">{issue.culprit}</p>{/if}
    </div>
    <div class="flex gap-2">
      {#if issue.status !== 'resolved'}
        <button onclick={() => setStatus('resolved')} class="rounded-md border border-emerald-500/40 px-3 py-1.5 text-sm text-emerald-300 transition-colors hover:bg-emerald-500/10 active:translate-y-px">Resolve</button>
      {/if}
      {#if issue.status !== 'ignored'}
        <button onclick={() => setStatus('ignored')} class="rounded-md border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 transition-colors hover:bg-zinc-800 active:translate-y-px">Ignore</button>
      {/if}
      {#if issue.status !== 'unresolved'}
        <button onclick={() => setStatus('unresolved')} class="rounded-md border border-amber-400/40 px-3 py-1.5 text-sm text-amber-300 transition-colors hover:bg-amber-400/10 active:translate-y-px">Reopen</button>
      {/if}
    </div>
  </div>

  <div class="mt-6 grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-zinc-800/80 bg-zinc-800/40 sm:grid-cols-4">
    {#each [['Events', String(issue.event_count)], ['Status', issue.status], ['First seen', relativeTime(issue.first_seen)], ['Last seen', relativeTime(issue.last_seen)]] as [label, value] (label)}
      <div class="bg-zinc-950 px-4 py-3">
        <div class="text-xs text-zinc-500">{label}</div>
        <div class="mt-0.5 font-mono text-sm text-zinc-200 capitalize">{value}</div>
      </div>
    {/each}
  </div>

  {#if latest}
    <h2 class="mt-8 mb-2 text-sm font-medium text-zinc-300">Latest event</h2>
    {#if latest.exception_value || latest.message}
      <p class="mb-4 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-sm text-zinc-300">
        {latest.exception_value || latest.message}
      </p>
    {/if}

    {#if latest.stacktrace && latest.stacktrace.frames.length}
      <div class="overflow-hidden rounded-lg border border-zinc-800/80">
        <div class="border-b border-zinc-800/80 bg-zinc-900/40 px-4 py-2 text-xs font-medium text-zinc-400">Stack trace</div>
        <ul class="divide-y divide-zinc-800/60 font-mono text-[13px]">
          {#each [...latest.stacktrace.frames].reverse() as f, i (i)}
            <li class="flex items-baseline gap-3 px-4 py-2 {f.in_app === false ? 'opacity-50' : ''}">
              <span class="text-amber-300">{f.function || '?'}</span>
              <span class="truncate text-zinc-500">
                {f.module || f.filename || ''}{f.lineno ? `:${f.lineno}` : ''}
              </span>
              {#if f.in_app !== false}<span class="ml-auto shrink-0 text-[10px] uppercase text-zinc-600">in app</span>{/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    <div class="mt-4 flex flex-wrap gap-x-6 gap-y-1 text-xs text-zinc-500">
      {#if latest.environment}<span>env <span class="font-mono text-zinc-400">{latest.environment}</span></span>{/if}
      {#if latest.release}<span>release <span class="font-mono text-zinc-400">{latest.release}</span></span>{/if}
      {#if latest.platform}<span>platform <span class="font-mono text-zinc-400">{latest.platform}</span></span>{/if}
      <span>received <span class="font-mono text-zinc-400">{relativeTime(latest.received_at)}</span></span>
    </div>
  {/if}
{:else if error}
  <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{:else}
  <div class="flex min-h-[30vh] items-center justify-center text-sm text-zinc-500">Loading...</div>
{/if}
