<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';

  // Resolves the numeric DSN id from a provisioner deep-link (/go/<dsnID>) to
  // the project page, BEHIND the session.
  //
  // The backend redirect used to do the lookup itself and 302 to
  // /projects/<cuid> on a hit or /projects on a miss, which told an anonymous
  // caller whether a given dsn id existed and handed them the internal id when
  // it did. Resolving here means the answer depends on who is asking.
  const dsnID = $derived(page.params.dsnID ?? '');
  let error = $state<string | null>(null);

  onMount(async () => {
    try {
      const project = await api.projectByDsnID(dsnID);
      await goto(`/projects/${project.id}`, { replaceState: true });
    } catch {
      error = 'That project could not be found in this workspace.';
    }
  });
</script>

<div class="mx-auto max-w-md p-8 text-center">
  {#if error}
    <p class="text-sm text-red-600 dark:text-red-400">{error}</p>
    <a class="mt-4 inline-block text-sm underline" href="/projects">Back to projects</a>
  {:else}
    <p class="text-sm text-neutral-500">Opening project…</p>
  {/if}
</div>
