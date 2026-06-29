<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import type { Project } from '$lib/types';

  let projects = $state<Project[] | null>(null);
  let error = $state<string | null>(null);
  let creating = $state(false);
  let name = $state('');
  let platform = $state('javascript');
  let copied = $state<string | null>(null);

  const platforms = ['javascript', 'node', 'python', 'go', 'ruby', 'php', 'java', 'other'];

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(load);

  async function load() {
    try {
      projects = await api.projects();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load projects';
    }
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    creating = true;
    error = null;
    try {
      const p = await api.createProject(name, platform);
      projects = [p, ...(projects ?? [])];
      name = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to create project';
    } finally {
      creating = false;
    }
  }

  async function copy(dsn: string) {
    await navigator.clipboard.writeText(dsn);
    copied = dsn;
    setTimeout(() => (copied = copied === dsn ? null : copied), 1500);
  }
</script>

<div class="mb-8 flex items-end justify-between">
  <div>
    <h1 class="text-xl font-semibold tracking-tight">Projects</h1>
    <p class="mt-1 text-sm text-zinc-500">Each project has its own DSN. Point an SDK at it to start sending events.</p>
  </div>
</div>

<form onsubmit={create} class="mb-8 flex flex-wrap items-end gap-3 border-b border-zinc-800/80 pb-8">
  <div class="flex flex-1 flex-col gap-1.5">
    <label for="name" class="text-xs font-medium text-zinc-400">New project</label>
    <input
      id="name"
      bind:value={name}
      placeholder="payments-api"
      class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
    />
  </div>
  <div class="flex flex-col gap-1.5">
    <label for="platform" class="text-xs font-medium text-zinc-400">Platform</label>
    <select
      id="platform"
      bind:value={platform}
      class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
    >
      {#each platforms as p (p)}
        <option value={p}>{p}</option>
      {/each}
    </select>
  </div>
  <button
    type="submit"
    disabled={creating}
    class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
  >
    {creating ? 'Creating...' : 'Create'}
  </button>
</form>

{#if error}
  <p class="mb-6 rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{/if}

{#if projects === null}
  <div class="space-y-3">
    {#each Array(3) as _, i (i)}
      <div class="h-20 animate-pulse rounded-lg border border-zinc-800/60 bg-zinc-900/40"></div>
    {/each}
  </div>
{:else if projects.length === 0}
  <div class="rounded-lg border border-dashed border-zinc-800 px-6 py-14 text-center">
    <p class="text-sm font-medium text-zinc-300">No projects yet</p>
    <p class="mx-auto mt-1 max-w-sm text-sm text-zinc-500">
      Create your first project above. You will get a DSN to drop into your app.
    </p>
  </div>
{:else}
  <div class="grid gap-3 sm:grid-cols-2">
    {#each projects as p (p.id)}
      <div class="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-4">
        <div class="flex items-start justify-between">
          <a href="/projects/{p.id}" class="group">
            <div class="font-medium tracking-tight group-hover:text-amber-300">{p.name}</div>
            <div class="mt-0.5 text-xs text-zinc-500">{p.platform}</div>
          </a>
          <a
            href="/projects/{p.id}"
            class="rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-400 transition-colors hover:border-zinc-700 hover:text-zinc-200"
          >
            Issues
          </a>
        </div>
        <div class="mt-3 flex items-center gap-2">
          <code class="flex-1 truncate rounded border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-400">{p.dsn}</code>
          <button
            onclick={() => copy(p.dsn)}
            class="shrink-0 rounded-md border border-zinc-800 px-2 py-1.5 text-xs text-zinc-400 transition-colors hover:border-zinc-700 hover:text-zinc-200 active:translate-y-px"
          >
            {copied === p.dsn ? 'Copied' : 'Copy DSN'}
          </button>
        </div>
      </div>
    {/each}
  </div>
{/if}
