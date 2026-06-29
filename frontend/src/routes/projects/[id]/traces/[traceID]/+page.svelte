<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import type { Span } from '$lib/types';

  const projectID = $derived(page.params.id ?? '');
  const traceID = $derived(page.params.traceID ?? '');

  let spans = $state<Span[] | null>(null);
  let error = $state<string | null>(null);

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(async () => {
    try {
      const data = await api.trace(projectID, traceID);
      spans = data.slice().sort((a, b) => a.start_unix_ms - b.start_unix_ms);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load trace';
      spans = [];
    }
  });

  const t0 = $derived(spans && spans.length ? Math.min(...spans.map((s) => s.start_unix_ms)) : 0);
  const total = $derived(
    spans && spans.length
      ? Math.max(1, Math.max(...spans.map((s) => s.start_unix_ms + s.duration_ms)) - t0)
      : 1
  );

  function depth(s: Span): number {
    if (!spans) return 0;
    const byId = new Map(spans.map((x) => [x.span_id, x]));
    let d = 0;
    let p = s.parent_span_id;
    while (p && byId.has(p) && d < 30) {
      d++;
      p = byId.get(p)!.parent_span_id;
    }
    return d;
  }

  function fmtDur(ms: number): string {
    if (ms < 1) return '<1ms';
    if (ms < 1000) return `${Math.round(ms)}ms`;
    return `${(ms / 1000).toFixed(2)}s`;
  }
</script>

<div class="mb-6 flex items-center gap-2 text-sm text-zinc-500">
  <a href="/projects" class="hover:text-zinc-300">Projects</a>
  <span>/</span>
  <a href="/projects/{projectID}/traces" class="hover:text-zinc-300">Traces</a>
  <span>/</span>
  <span class="text-zinc-300">Trace</span>
</div>

{#if spans && spans.length}
  <div class="mb-1 flex items-baseline justify-between">
    <h1 class="text-lg font-semibold tracking-tight">{spans[0].name}</h1>
    <span class="font-mono text-sm text-zinc-400">{fmtDur(total)}</span>
  </div>
  <p class="mb-6 font-mono text-xs text-zinc-600">{traceID}</p>

  <div class="overflow-hidden rounded-lg border border-zinc-800/80">
    <ul class="divide-y divide-zinc-800/40">
      {#each spans as s (s.span_id)}
        {@const left = ((s.start_unix_ms - t0) / total) * 100}
        {@const width = Math.max(0.5, (s.duration_ms / total) * 100)}
        {@const err = s.status === 'error'}
        <li class="grid grid-cols-[minmax(0,2fr)_3fr] items-center gap-3 px-4 py-2 hover:bg-zinc-900/40">
          <div class="min-w-0" style="padding-left: {depth(s) * 14}px">
            <div class="truncate text-sm text-zinc-200">{s.name}</div>
            <div class="truncate font-mono text-[11px] text-zinc-600">{s.kind || 'internal'}{err ? ' · error' : ''}</div>
          </div>
          <div class="relative h-6">
            <div
              class="absolute top-1/2 h-3 -translate-y-1/2 rounded-sm {err ? 'bg-rose-400/80' : 'bg-amber-400/80'}"
              style="left: {left}%; width: {width}%"
              title="{fmtDur(s.duration_ms)}"
            ></div>
            <span
              class="absolute top-1/2 -translate-y-1/2 font-mono text-[11px] text-zinc-500"
              style="left: clamp(0%, {left + width}%, 88%); padding-left: 6px"
            >
              {fmtDur(s.duration_ms)}
            </span>
          </div>
        </li>
      {/each}
    </ul>
  </div>
{:else if error}
  <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{:else if spans}
  <div class="rounded-lg border border-dashed border-zinc-800 px-6 py-14 text-center text-sm text-zinc-500">Trace not found.</div>
{:else}
  <div class="flex min-h-[30vh] items-center justify-center text-sm text-zinc-500">Loading...</div>
{/if}
