<script lang="ts">
  import { goto } from '$app/navigation';
  import { session } from '$lib/session.svelte';

  $effect(() => {
    if (!session.loaded) return;
    goto(session.user ? '/overview' : '/login', { replaceState: true });
  });
</script>

<div class="flex min-h-[40vh] items-center justify-center text-sm text-zinc-500">Loading...</div>
