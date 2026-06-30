<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, ApiError } from '$lib/api';

  const token = $derived(page.url.searchParams.get('token') ?? '');

  let password = $state('');
  let confirm = $state('');
  let busy = $state(false);
  let done = $state(false);
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
      await api.resetPassword(token, password);
      done = true;
      setTimeout(() => goto('/login', { replaceState: true }), 1500);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Something went wrong';
    } finally {
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

    {#if done}
      <h2 class="mb-1 text-lg font-medium">Password updated</h2>
      <p class="mb-6 text-sm text-zinc-500">Taking you to sign in...</p>
    {:else if !token}
      <h2 class="mb-1 text-lg font-medium">Invalid link</h2>
      <p class="mb-6 text-sm text-zinc-500">This reset link is missing its token. Request a new one.</p>
      <a href="/forgot-password" class="text-sm text-amber-400 transition-colors hover:text-amber-300">Request a reset link</a>
    {:else}
      <h2 class="mb-1 text-lg font-medium">Set a new password</h2>
      <p class="mb-6 text-sm text-zinc-500">Choose a strong password (at least 8 characters).</p>

      <form onsubmit={submit} class="flex flex-col gap-4">
        <div class="flex flex-col gap-1.5">
          <label for="password" class="text-xs font-medium text-zinc-400">New password</label>
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
          {busy ? 'Updating...' : 'Update password'}
        </button>
      </form>
    {/if}
  </div>
</div>
