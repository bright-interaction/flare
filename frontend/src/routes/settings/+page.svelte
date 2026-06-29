<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import type { Channel } from '$lib/types';

  let channels = $state<Channel[] | null>(null);
  let error = $state<string | null>(null);
  let type = $state('log');
  let url = $state('');
  let busy = $state(false);

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(load);

  async function load() {
    try {
      channels = await api.channels();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load channels';
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    try {
      const config = type === 'webhook' ? { url } : {};
      const ch = await api.createChannel(type, config);
      channels = [ch, ...(channels ?? [])];
      url = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create channel';
    } finally {
      busy = false;
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
      <option value="webhook">Webhook</option>
    </select>
  </div>
  {#if type === 'webhook'}
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
      </li>
    {/each}
  </ul>
{/if}
