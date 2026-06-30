<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { relativeTime } from '$lib/format';
  import type { Invite, Member } from '$lib/types';

  let members = $state<Member[] | null>(null);
  let invites = $state<Invite[]>([]);
  let error = $state<string | null>(null);

  let inviteEmail = $state('');
  let inviteRole = $state('member');
  let inviting = $state(false);
  let lastLink = $state<string | null>(null);
  let copied = $state(false);

  const roles = ['viewer', 'member', 'admin', 'owner'];
  const myRole = $derived(session.user?.role ?? 'viewer');
  const isAdmin = $derived(myRole === 'admin' || myRole === 'owner');

  $effect(() => {
    if (session.loaded && !session.user) goto('/login', { replaceState: true });
  });

  onMount(load);

  async function load() {
    try {
      members = await api.members();
      if (isAdmin) invites = await api.invites();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to load team';
    }
  }

  async function invite(e: SubmitEvent) {
    e.preventDefault();
    if (!inviteEmail.trim()) return;
    inviting = true;
    error = null;
    lastLink = null;
    try {
      const inv = await api.inviteMember(inviteEmail.trim(), inviteRole);
      invites = [{ ...inv, accept_url: undefined }, ...invites];
      lastLink = inv.accept_url ?? null;
      inviteEmail = '';
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to send invite';
    } finally {
      inviting = false;
    }
  }

  async function changeRole(m: Member, role: string) {
    error = null;
    try {
      await api.updateMemberRole(m.id, role);
      members = (members ?? []).map((x) => (x.id === m.id ? { ...x, role } : x));
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to change role';
      members = await api.members(); // reset to server truth
    }
  }

  async function remove(m: Member) {
    if (!confirm(`Remove ${m.email} from the workspace?`)) return;
    error = null;
    try {
      await api.removeMember(m.id);
      members = (members ?? []).filter((x) => x.id !== m.id);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to remove member';
    }
  }

  async function revoke(inv: Invite) {
    try {
      await api.revokeInvite(inv.id);
      invites = invites.filter((i) => i.id !== inv.id);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Failed to revoke invite';
    }
  }

  async function copyLink() {
    if (!lastLink) return;
    await navigator.clipboard.writeText(lastLink);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }

  function roleClass(role: string) {
    return role === 'owner'
      ? 'bg-amber-400/10 text-amber-300'
      : role === 'admin'
        ? 'bg-sky-400/10 text-sky-300'
        : role === 'viewer'
          ? 'bg-zinc-700/40 text-zinc-400'
          : 'bg-emerald-400/10 text-emerald-300';
  }
</script>

<div class="mb-8">
  <h1 class="text-xl font-semibold tracking-tight">Team</h1>
  <p class="mt-1 text-sm text-zinc-500">
    Members and their roles. Viewer reads only; member manages projects and issues; admin manages the team; owner manages everything.
  </p>
</div>

{#if error}
  <p class="mb-6 rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
{/if}

{#if isAdmin}
  <form onsubmit={invite} class="mb-4 flex flex-wrap items-end gap-3 border-b border-zinc-800/80 pb-8">
    <div class="flex flex-1 flex-col gap-1.5">
      <label for="invemail" class="text-xs font-medium text-zinc-400">Invite by email</label>
      <input
        id="invemail"
        type="email"
        bind:value={inviteEmail}
        placeholder="teammate@company.com"
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
      />
    </div>
    <div class="flex flex-col gap-1.5">
      <label for="invrole" class="text-xs font-medium text-zinc-400">Role</label>
      <select
        id="invrole"
        bind:value={inviteRole}
        class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm capitalize outline-none focus:border-amber-400/60"
      >
        {#each roles as r (r)}
          {#if r !== 'owner' || myRole === 'owner'}<option value={r}>{r}</option>{/if}
        {/each}
      </select>
    </div>
    <button
      type="submit"
      disabled={inviting}
      class="rounded-md bg-amber-400 px-3.5 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
    >
      {inviting ? 'Inviting...' : 'Send invite'}
    </button>
  </form>

  {#if lastLink}
    <div class="mb-6 rounded-md border border-amber-400/40 bg-amber-400/10 px-4 py-3">
      <p class="text-sm font-medium text-amber-200">Invite created. Share this link:</p>
      <div class="mt-2 flex items-center gap-2">
        <code class="flex-1 truncate rounded border border-amber-400/30 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-amber-100">{lastLink}</code>
        <button onclick={copyLink} class="shrink-0 rounded-md border border-amber-400/40 px-2 py-1.5 text-xs text-amber-200 hover:bg-amber-400/10">
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  {/if}
{/if}

{#if members === null}
  <div class="space-y-2">
    {#each Array(3) as _, i (i)}
      <div class="h-12 animate-pulse rounded-md border border-zinc-800/60 bg-zinc-900/40"></div>
    {/each}
  </div>
{:else}
  <ul class="divide-y divide-zinc-800/60">
    {#each members as m (m.id)}
      <li class="flex items-center gap-3 py-3">
        <span class="text-sm text-zinc-200">{m.email}</span>
        {#if m.is_you}<span class="text-[10px] uppercase text-zinc-600">you</span>{/if}
        <div class="ml-auto flex items-center gap-2">
          {#if isAdmin && !m.is_you}
            <select
              value={m.role}
              onchange={(e) => changeRole(m, (e.target as HTMLSelectElement).value)}
              class="rounded-md border border-zinc-800 bg-zinc-900 px-2 py-1 text-xs capitalize outline-none focus:border-amber-400/60"
            >
              {#each roles as r (r)}
                {#if r !== 'owner' || myRole === 'owner'}<option value={r}>{r}</option>{/if}
              {/each}
            </select>
            <button onclick={() => remove(m)} class="text-xs text-zinc-600 transition-colors hover:text-rose-400">Remove</button>
          {:else}
            <span class="rounded px-1.5 py-0.5 text-[11px] font-medium capitalize {roleClass(m.role)}">{m.role}</span>
          {/if}
        </div>
      </li>
    {/each}
  </ul>

  {#if isAdmin && invites.length}
    <h2 class="mt-12 mb-3 text-sm font-medium text-zinc-300">Pending invites</h2>
    <ul class="divide-y divide-zinc-800/60">
      {#each invites as inv (inv.id)}
        <li class="flex items-center gap-3 py-2.5 text-sm">
          <span class="text-zinc-300">{inv.email}</span>
          <span class="rounded px-1.5 py-0.5 text-[11px] font-medium capitalize {roleClass(inv.role)}">{inv.role}</span>
          <span class="text-xs text-zinc-600">invited {relativeTime(inv.created_at)}</span>
          <button onclick={() => revoke(inv)} class="ml-auto text-xs text-zinc-600 transition-colors hover:text-rose-400">Revoke</button>
        </li>
      {/each}
    </ul>
  {/if}
{/if}
