<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';

  const token = $derived(page.url.searchParams.get('token') ?? '');

  let password = $state('');
  let confirm = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    if (password !== confirm) {
      error = 'Passwords do not match';
      return;
    }
    busy = true;
    try {
      session.user = await api.acceptInvite(token, password);
      goto('/projects', { replaceState: true });
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Something went wrong';
      busy = false;
    }
  }
</script>

<div class="flex min-h-[70vh] items-center justify-center">
  <div class="w-full max-w-sm">
    <div class="mb-8 flex items-center gap-2.5">
      <span class="inline-block h-3 w-3 rounded-full bg-amber-400 shadow-[0_0_12px_3px_rgba(251,191,36,0.4)]"></span>
      <h1 class="text-xl font-semibold tracking-tight">Flare</h1>
    </div>

    {#if !token}
      <h2 class="mb-1 text-lg font-medium">Invalid invite</h2>
      <p class="mb-6 text-sm text-zinc-500">This invite link is missing its token. Ask your admin to send a new one.</p>
      <a href="/login" class="text-sm text-amber-400 transition-colors hover:text-amber-300">Back to sign in</a>
    {:else}
      <h2 class="mb-1 text-lg font-medium">Join the workspace</h2>
      <p class="mb-6 text-sm text-zinc-500">Set a password to accept your invite and join the team.</p>

      <form onsubmit={submit} class="flex flex-col gap-4">
        <div class="flex flex-col gap-1.5">
          <label for="password" class="text-xs font-medium text-zinc-400">Password</label>
          <input
            id="password"
            type="password"
            required
            minlength={8}
            bind:value={password}
            autocomplete="new-password"
            class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
          />
        </div>
        <div class="flex flex-col gap-1.5">
          <label for="confirm" class="text-xs font-medium text-zinc-400">Confirm password</label>
          <input
            id="confirm"
            type="password"
            required
            minlength={8}
            bind:value={confirm}
            autocomplete="new-password"
            class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
          />
        </div>

        {#if error}
          <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
        {/if}

        <button
          type="submit"
          disabled={busy}
          class="mt-1 rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
        >
          {busy ? 'Joining...' : 'Accept invite'}
        </button>
      </form>
    {/if}
  </div>
</div>
