<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { session } from '$lib/session.svelte';

  let mode = $state<'login' | 'register'>('login');
  let email = $state('');
  let password = $state('');
  let orgName = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      session.user =
        mode === 'login'
          ? await api.login(email, password)
          : await api.register(email, password, orgName);
      goto('/projects', { replaceState: true });
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

    <h2 class="mb-1 text-lg font-medium">
      {mode === 'login' ? 'Sign in' : 'Create your workspace'}
    </h2>
    <p class="mb-6 text-sm text-zinc-500">
      {mode === 'login' ? 'Welcome back.' : 'Errors, logs, and traces on infrastructure you own.'}
    </p>

    <form onsubmit={submit} class="flex flex-col gap-4">
      {#if mode === 'register'}
        <div class="flex flex-col gap-1.5">
          <label for="org" class="text-xs font-medium text-zinc-400">Workspace name</label>
          <input
            id="org"
            bind:value={orgName}
            placeholder="Northwind Engineering"
            class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
          />
        </div>
      {/if}
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
      <div class="flex flex-col gap-1.5">
        <label for="password" class="text-xs font-medium text-zinc-400">Password</label>
        <input
          id="password"
          type="password"
          required
          minlength={8}
          bind:value={password}
          autocomplete={mode === 'login' ? 'current-password' : 'new-password'}
          class="rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
        />
      </div>

      {#if error}
        <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">
          {error}
        </p>
      {/if}

      <button
        type="submit"
        disabled={busy}
        class="mt-1 rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition-colors hover:bg-amber-300 active:translate-y-px disabled:opacity-60"
      >
        {busy ? 'Working...' : mode === 'login' ? 'Sign in' : 'Create workspace'}
      </button>
    </form>

    <button
      class="mt-5 text-sm text-zinc-500 transition-colors hover:text-zinc-300"
      onclick={() => {
        mode = mode === 'login' ? 'register' : 'login';
        error = null;
      }}
    >
      {mode === 'login' ? 'Need an account? Create one' : 'Have an account? Sign in'}
    </button>
  </div>
</div>
