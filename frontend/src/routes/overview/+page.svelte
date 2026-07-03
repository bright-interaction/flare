<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime, levelColor } from '$lib/format';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import VolumeChart from '$lib/components/VolumeChart.svelte';
  import type { Overview } from '$lib/types';

  let data = $state<Overview | null>(null);
  let error = $state<string | null>(null);

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(load);

  async function load() {
    try {
      data = await api.overview();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load the overview';
    }
  }

  function statusColor(s: string): string {
    if (s === 'spike') return 'bg-rose-400 shadow-[0_0_8px_2px_rgba(251,113,133,0.5)]';
    if (s === 'quiet') return 'bg-zinc-600';
    return 'bg-emerald-400';
  }

  const nf = new Intl.NumberFormat();
</script>

<div class="mb-8 flex items-end justify-between">
  <div>
    <h1 class="text-xl font-semibold tracking-tight">Overview</h1>
    <p class="mt-1 text-sm text-zinc-500">Error activity across every project.</p>
  </div>
  <span class="rounded-full border border-zinc-800 px-2.5 py-1 text-xs text-zinc-500">Last 24 hours</span>
</div>

{#if error}
  <div class="rounded-lg border border-rose-900/50 bg-rose-950/20 p-5 text-sm text-rose-300">{error}</div>
{:else if !data}
  <!-- Loading: skeleton matching the real layout -->
  <div class="animate-pulse space-y-6">
    <div class="grid grid-cols-3 overflow-hidden rounded-lg border border-zinc-800/80 bg-zinc-900/40">
      {#each Array(3) as _, i (i)}
        <div class="border-r border-zinc-800/80 p-5 last:border-r-0">
          <div class="h-3 w-20 rounded bg-zinc-800"></div>
          <div class="mt-3 h-7 w-16 rounded bg-zinc-800"></div>
        </div>
      {/each}
    </div>
    <div class="h-[210px] rounded-lg border border-zinc-800/80 bg-zinc-900/40"></div>
    <div class="grid gap-6 md:grid-cols-2">
      <div class="h-64 rounded-lg border border-zinc-800/80 bg-zinc-900/40"></div>
      <div class="h-64 rounded-lg border border-zinc-800/80 bg-zinc-900/40"></div>
    </div>
  </div>
{:else}
  <!-- Headline stats: one divided panel, not three floating cards -->
  <div class="mb-6 grid grid-cols-3 overflow-hidden rounded-lg border border-zinc-800/80 bg-zinc-900/40">
    <div class="border-r border-zinc-800/80 p-5">
      <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Events (24h)</div>
      <div class="mt-2 font-mono text-3xl font-semibold tracking-tight text-zinc-100">{nf.format(data.events_24h)}</div>
    </div>
    <div class="border-r border-zinc-800/80 p-5">
      <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Unresolved</div>
      <div class="mt-2 font-mono text-3xl font-semibold tracking-tight text-zinc-100">{nf.format(data.unresolved)}</div>
    </div>
    <div class="p-5">
      <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">New today</div>
      <div class="mt-2 font-mono text-3xl font-semibold tracking-tight {data.new_today > 0 ? 'text-amber-400' : 'text-zinc-100'}">
        {nf.format(data.new_today)}
      </div>
    </div>
  </div>

  <!-- Volume chart: gallery-style label outside the surface -->
  <div class="mb-6 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-5">
    <div class="mb-3 text-xs font-medium uppercase tracking-wide text-zinc-500">Error volume</div>
    <VolumeChart buckets={data.volume} />
  </div>

  <div class="grid gap-6 md:grid-cols-2">
    <!-- Top issues -->
    <section class="rounded-lg border border-zinc-800/80 bg-zinc-900/40">
      <div class="border-b border-zinc-800/80 px-5 py-3 text-xs font-medium uppercase tracking-wide text-zinc-500">
        Top issues
      </div>
      {#if data.top_issues.length === 0}
        <div class="px-5 py-10 text-center text-sm text-zinc-600">No unresolved issues. All clear.</div>
      {:else}
        <ul class="divide-y divide-zinc-800/60">
          {#each data.top_issues as issue (issue.id)}
            <li>
              <a
                href={`/issues/${issue.id}`}
                class="flex items-center gap-3 px-5 py-3 transition-colors hover:bg-zinc-800/30"
              >
                <span class="text-[10px] font-medium uppercase {levelColor(issue.level)}">{issue.level}</span>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm text-zinc-200">{issue.title}</div>
                  <div class="truncate text-xs text-zinc-600">{issue.project_name}</div>
                </div>
                <span class="font-mono text-sm text-zinc-400">{nf.format(issue.event_count)}</span>
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <!-- Project health -->
    <section class="rounded-lg border border-zinc-800/80 bg-zinc-900/40">
      <div class="border-b border-zinc-800/80 px-5 py-3 text-xs font-medium uppercase tracking-wide text-zinc-500">
        Projects
      </div>
      {#if data.projects.length === 0}
        <div class="px-5 py-10 text-center text-sm text-zinc-600">
          No projects yet. <a href="/projects" class="text-amber-400 hover:text-amber-300">Create one</a> to start.
        </div>
      {:else}
        <ul class="divide-y divide-zinc-800/60">
          {#each data.projects as p (p.id)}
            <li>
              <a
                href={`/projects/${p.id}`}
                class="flex items-center gap-4 px-5 py-3 transition-colors hover:bg-zinc-800/30"
              >
                <span class="h-2 w-2 shrink-0 rounded-full {statusColor(p.status)}" title={p.status}></span>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm text-zinc-200">{p.name}</div>
                  <div class="text-xs text-zinc-600">
                    <span class="font-mono text-zinc-500">{nf.format(p.events_24h)}</span> events -
                    <span class="font-mono text-zinc-500">{nf.format(p.unresolved)}</span> open
                  </div>
                </div>
                <div class="h-8 w-24 shrink-0 text-zinc-500">
                  <Sparkline values={p.volume} accent={p.status === 'spike' ? '#fb7185' : '#fbbf24'} />
                </div>
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </div>
{/if}
