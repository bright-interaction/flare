<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeUnix, levelColor } from '$lib/format';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import VolumeChart from '$lib/components/VolumeChart.svelte';
  import type { Overview, OverviewProject } from '$lib/types';

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

  const nf = new Intl.NumberFormat();

  // Group cards by health so the panel reads as "what needs me / what is fine /
  // what is not reporting" instead of one long undifferentiated list. Attention
  // is open by default; the healthy and silent tails collapse to a count.
  let showAttention = $state(true);
  let showHealthy = $state(false);
  let showSilent = $state(false);

  const groups = $derived.by(() => {
    const attention: OverviewProject[] = [];
    const healthy: OverviewProject[] = [];
    const silent: OverviewProject[] = [];
    for (const p of data?.projects ?? []) {
      if (p.status === 'spike' || p.status === 'alert') attention.push(p);
      else if (p.status === 'silent') silent.push(p);
      else healthy.push(p);
    }
    return { attention, healthy, silent };
  });

  // Dot color per health state: rose (surging) / amber (erroring) / grey
  // (no pulse) / emerald (alive and clean).
  function dotColor(s: string): string {
    switch (s) {
      case 'spike':
        return 'bg-rose-400 shadow-[0_0_8px_2px_rgba(251,113,133,0.55)]';
      case 'alert':
        return 'bg-amber-400';
      case 'silent':
        return 'bg-zinc-600';
      default:
        return 'bg-emerald-400';
    }
  }

  function sparkAccent(s: string): string {
    return s === 'spike' ? '#fb7185' : s === 'alert' ? '#fbbf24' : '#34d399';
  }

  // Second line under the project name: liveness first for silent/healthy (the
  // pulse is the answer to "is it up?"), error counts for anything erroring.
  function subline(p: OverviewProject): string {
    if (p.status === 'silent') {
      return p.last_seen_unix ? `no pulse · last seen ${relativeUnix(p.last_seen_unix)}` : 'never reported';
    }
    if (p.status === 'healthy') {
      return p.last_seen_unix ? `healthy · seen ${relativeUnix(p.last_seen_unix)}` : 'healthy';
    }
    return `${nf.format(p.events_24h)} error${p.events_24h === 1 ? '' : 's'} · ${nf.format(p.unresolved)} open`;
  }
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
  {#if data.alerting_blind}
    <div class="mb-6 flex items-start gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-4">
      <span class="mt-1 inline-block h-2 w-2 shrink-0 rounded-full bg-amber-400"></span>
      <div class="text-sm">
        <p class="font-medium text-amber-300">Alerts have nowhere to go</p>
        <p class="mt-0.5 text-amber-200/70">
          You have active alert rules but no enabled notification channel, so alerts fire into the void and
          reach no one. <a href="/settings" class="underline underline-offset-2 hover:text-amber-100">Add a
          channel</a> in settings and send a test to confirm it delivers.
        </p>
      </div>
    </div>
  {/if}
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

    <!-- Project health: grouped by pulse + error state, collapsible tails -->
    <section class="rounded-lg border border-zinc-800/80 bg-zinc-900/40">
      <div class="flex items-center justify-between gap-3 border-b border-zinc-800/80 px-5 py-3">
        <span class="text-xs font-medium uppercase tracking-wide text-zinc-500">Projects</span>
        <span class="hidden text-[11px] text-zinc-600 sm:flex sm:items-center sm:gap-3">
          <span class="flex items-center gap-1"><span class="h-1.5 w-1.5 rounded-full bg-emerald-400"></span>live</span>
          <span class="flex items-center gap-1"><span class="h-1.5 w-1.5 rounded-full bg-amber-400"></span>errors</span>
          <span class="flex items-center gap-1"><span class="h-1.5 w-1.5 rounded-full bg-zinc-600"></span>no pulse</span>
        </span>
      </div>
      {#if data.projects.length === 0}
        <div class="px-5 py-10 text-center text-sm text-zinc-600">
          No projects yet. <a href="/projects" class="text-amber-400 hover:text-amber-300">Create one</a> to start.
        </div>
      {:else}
        {#snippet projectRow(p: OverviewProject)}
          <li>
            <a
              href={`/projects/${p.id}`}
              class="flex items-center gap-3 px-5 py-2.5 transition-colors hover:bg-zinc-800/30"
            >
              <span class="h-2 w-2 shrink-0 rounded-full {dotColor(p.status)}" title={p.status}></span>
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm text-zinc-200">{p.name}</div>
                <div class="truncate text-xs text-zinc-600">{subline(p)}</div>
              </div>
              {#if p.status === 'silent'}
                <span class="shrink-0 text-xs text-zinc-700" aria-hidden="true">-</span>
              {:else}
                <div class="h-7 w-20 shrink-0 text-zinc-500">
                  <Sparkline values={p.volume} accent={sparkAccent(p.status)} />
                </div>
              {/if}
            </a>
          </li>
        {/snippet}

        {#snippet groupHeader(label: string, count: number, dot: string, open: boolean, toggle: () => void, first: boolean)}
          <button
            type="button"
            onclick={toggle}
            class="flex w-full items-center gap-2.5 px-5 py-2.5 text-left transition-colors hover:bg-zinc-800/20 {first
              ? ''
              : 'border-t border-zinc-800/60'}"
          >
            <span class="h-2 w-2 shrink-0 rounded-full {dot}"></span>
            <span class="text-xs font-medium uppercase tracking-wide text-zinc-300">{label}</span>
            <span class="rounded-full bg-zinc-800 px-1.5 text-[11px] text-zinc-400">{count}</span>
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              class="ml-auto h-3.5 w-3.5 text-zinc-600 transition-transform {open ? 'rotate-90' : ''}"
              aria-hidden="true"
            >
              <path d="M9 6l6 6-6 6" />
            </svg>
          </button>
        {/snippet}

        <!-- Needs attention: alive but erroring or spiking. Open by default. -->
        {#if groups.attention.length}
          {@render groupHeader('Needs attention', groups.attention.length, 'bg-rose-400', showAttention, () => (showAttention = !showAttention), true)}
          {#if showAttention}
            <ul class="divide-y divide-zinc-800/60 border-t border-zinc-800/40">
              {#each groups.attention as p (p.id)}{@render projectRow(p)}{/each}
            </ul>
          {/if}
        {:else}
          <div class="flex items-center gap-2 px-5 py-2.5 text-xs text-zinc-600">
            <span class="h-2 w-2 shrink-0 rounded-full bg-emerald-400/70"></span>
            Nothing needs attention.
          </div>
        {/if}

        <!-- Healthy: alive and clean. Collapsed to a count by default. -->
        {#if groups.healthy.length}
          {@render groupHeader('Healthy', groups.healthy.length, 'bg-emerald-400', showHealthy, () => (showHealthy = !showHealthy), false)}
          {#if showHealthy}
            <ul class="divide-y divide-zinc-800/60 border-t border-zinc-800/40">
              {#each groups.healthy as p (p.id)}{@render projectRow(p)}{/each}
            </ul>
          {/if}
        {/if}

        <!-- No pulse: silent or never reported. Collapsed to a count by default. -->
        {#if groups.silent.length}
          {@render groupHeader('No pulse', groups.silent.length, 'bg-zinc-600', showSilent, () => (showSilent = !showSilent), false)}
          {#if showSilent}
            <ul class="divide-y divide-zinc-800/60 border-t border-zinc-800/40">
              {#each groups.silent as p (p.id)}{@render projectRow(p)}{/each}
            </ul>
          {/if}
        {/if}
      {/if}
    </section>
  </div>
{/if}
