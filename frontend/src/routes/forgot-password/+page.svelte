<script lang="ts">
  import { api, ApiError } from '$lib/api';

  let email = $state('');
  let busy = $state(false);
  let sent = $state(false);
  let error = $state<string | null>(null);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      await api.forgotPassword(email);
      sent = true;
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

    {#if sent}
      <h2 class="mb-1 text-lg font-medium">Check your email</h2>
      <p class="mb-6 text-sm text-zinc-500">
        If an account exists for <span class="text-zinc-300">{email}</span>, a reset link is on its way. It expires in one hour.
      </p>
      <a href="/login" class="text-sm text-amber-400 transition-colors hover:text-amber-300">Back to sign in</a>
    {:else}
      <h2 class="mb-1 text-lg font-medium">Reset your password</h2>
      <p class="mb-6 text-sm text-zinc-500">Enter your email and we will send you a reset link.</p>

      <form onsubmit={submit} class="flex flex-col gap-4">
        <div class="flex flex-col gap-1.5">
          <label for="email" class="text-xs font-medium text-zinc-400">Email</label>
          <input
            id="email"
            type="email"
            required
            bind:value={email}
            autocomplete="email"
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
          {busy ? 'Sending...' : 'Send reset link'}
        </button>
      </form>

      <a href="/login" class="mt-5 inline-block text-sm text-zinc-500 transition-colors hover:text-zinc-300">Back to sign in</a>
    {/if}
  </div>
</div>
