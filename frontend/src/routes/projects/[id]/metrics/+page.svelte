<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime } from '$lib/format';
  import type { MetricName, MetricPoint, Project } from '$lib/types';

  const id = $derived(page.params.id ?? '');

  let project = $state<Project | null>(null);
  let names = $state<MetricName[] | null>(null);
  let error = $state<string | null>(null);
  let selected = $state<string | null>(null);
  let points = $state<MetricPoint[]>([]);
  let windowMin = $state(60);
  let loadingSeries = $state(false);

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(async () => {
    try {
      project = await api.project(id);
      names = await api.metrics(id);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load metrics';
      names = [];
    }
  });

  async function select(name: string) {
    selected = name;
    loadingSeries = true;
    try {
      points = await api.metricSeries(id, name, windowMin);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load series';
      points = [];
    } finally {
      loadingSeries = false;
    }
  }

  function changeWindow(w: number) {
    windowMin = w;
    if (selected) select(selected);
  }

  // Chart geometry (viewBox 100x30, padded), computed from the loaded points.
  const stats = $derived.by(() => {
    if (!points.length) return null;
    const vals = points.map((p) => p.value);
    const min = Math.min(...vals);
    const max = Math.max(...vals);
    const avg = vals.reduce((a, b) => a + b, 0) / vals.length;
    return { min, max, avg, latest: vals[vals.length - 1], count: vals.length };
  });

  const path = $derived.by(() => {
    if (points.length < 2 || !stats) return '';
    const { min, max } = stats;
    const span = max - min || 1;
    const n = points.length;
    return points
      .map((p, i) => {
        const x = (i / (n - 1)) * 98 + 1;
        const y = 29 - ((p.value - min) / span) * 28;
        return `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(' ');
  });

  function fmt(n: number): string {
    if (Math.abs(n) >= 1000 || (n !== 0 && Math.abs(n) < 0.01)) return n.toExponential(2);
    return Number.isInteger(n) ? String(n) : n.toFixed(2);
  }
</script>

{#if project}
  <div class="mb-6 flex items-center gap-2 text-sm text-zinc-500">
    <a href="/projects" class="hover:text-zinc-300">Projects</a>
    <span>/</span>
    <span class="text-zinc-300">{project.name}</span>
  </div>

  <div class="mb-5 flex items-center gap-1 border-b border-zinc-800/80">
    <a href="/projects/{id}" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Issues</a>
    <a href="/projects/{id}/logs" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Logs</a>
    <a href="/projects/{id}/traces" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Traces</a>
    <a href="/projects/{id}/metrics" class="-mb-px border-b-2 border-amber-400 px-3 py-2 text-sm text-zinc-100">Metrics</a>
    <a href="/projects/{id}/releases" class="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-zinc-500 hover:text-zinc-300">Releases</a>
  </div>

  {#if names === null}
    <div class="divide-y divide-zinc-800/60">
      {#each Array(5) as _, i (i)}<div class="h-10 animate-pulse bg-zinc-900/30"></div>{/each}
    </div>
  {:else if names.length === 0}
    <div class="rounded-lg border border-dashed border-zinc-800 px-6 py-14 text-center">
      <p class="text-sm font-medium text-zinc-300">No metrics</p>
      <p class="mx-auto mt-1 max-w-md text-sm text-zinc-500">
        Send metrics via OTLP to <code class="font-mono">{project.otlp_endpoint}/v1/metrics</code>, or POST a JSON
        array of <code class="font-mono">{'{name, value, kind, labels}'}</code> to the native
        <code class="font-mono">/metrics</code> endpoint with header <code class="font-mono">x-flare-key</code>.
      </p>
    </div>
  {:else}
    <div class="grid gap-6 md:grid-cols-[minmax(0,16rem)_1fr]">
      <ul class="divide-y divide-zinc-800/60 self-start rounded-lg border border-zinc-800/80">
        {#each names as m (m.name)}
          <li>
            <button
              onclick={() => select(m.name)}
              class="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-zinc-900/40 {selected === m.name ? 'bg-zinc-900/60' : ''}"
            >
              <div class="min-w-0 flex-1">
                <div class="truncate font-mono text-[13px] text-zinc-200">{m.name}</div>
                <div class="text-[11px] text-zinc-600">{m.kind} · {m.points} pts · {relativeTime(m.last_seen)}</div>
              </div>
            </button>
          </li>
        {/each}
      </ul>

      <div>
        {#if !selected}
          <div class="flex min-h-[16rem] items-center justify-center rounded-lg border border-dashed border-zinc-800 text-sm text-zinc-500">
            Select a metric to chart it.
          </div>
        {:else}
          <div class="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-5">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
              <h2 class="font-mono text-sm text-zinc-200">{selected}</h2>
              <div class="flex items-center gap-1 text-xs">
                {#each [{ l: '1h', w: 60 }, { l: '6h', w: 360 }, { l: '24h', w: 1440 }] as opt (opt.w)}
                  <button
                    onclick={() => changeWindow(opt.w)}
                    class="rounded px-2 py-1 {windowMin === opt.w ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'}"
                  >
                    {opt.l}
                  </button>
                {/each}
              </div>
            </div>

            {#if loadingSeries}
              <div class="h-40 animate-pulse rounded bg-zinc-900/40"></div>
            {:else if !stats}
              <p class="py-12 text-center text-sm text-zinc-500">No points in this window.</p>
            {:else}
              <svg viewBox="0 0 100 30" preserveAspectRatio="none" class="h-40 w-full">
                <path d={path} fill="none" stroke="#f59e0b" stroke-width="0.5" vector-effect="non-scaling-stroke" />
              </svg>
              <div class="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-5">
                {#each [['latest', stats.latest], ['min', stats.min], ['max', stats.max], ['avg', stats.avg], ['points', stats.count]] as [label, value] (label)}
                  <div class="rounded-md border border-zinc-800/80 px-3 py-2">
                    <div class="text-[10px] uppercase tracking-wide text-zinc-500">{label}</div>
                    <div class="mt-0.5 font-mono text-sm text-zinc-200">{label === 'points' ? value : fmt(value as number)}</div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}
{:else if error}
  <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{:else}
  <div class="flex min-h-[30vh] items-center justify-center text-sm text-zinc-500">Loading...</div>
{/if}
