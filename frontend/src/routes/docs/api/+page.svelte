<script lang="ts">
  import { onMount } from 'svelte';

  // The OpenAPI document is external JSON; a loose type is intentional here.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type Spec = any;

  let spec = $state<Spec | null>(null);
  let error = $state<string | null>(null);
  let q = $state('');
  let open = $state<Record<string, boolean>>({});

  const methodColor: Record<string, string> = {
    get: 'text-emerald-300 border-emerald-500/40',
    post: 'text-sky-300 border-sky-500/40',
    put: 'text-amber-300 border-amber-500/40',
    patch: 'text-amber-300 border-amber-500/40',
    delete: 'text-rose-300 border-rose-500/40'
  };
  const METHODS = ['get', 'post', 'put', 'patch', 'delete'];

  onMount(async () => {
    try {
      const res = await fetch('/openapi.json');
      if (!res.ok) throw new Error('spec unavailable');
      spec = await res.json();
    } catch (e) {
      error = e instanceof Error ? e.message : 'failed to load spec';
    }
  });

  interface Op {
    method: string;
    path: string;
    op: Spec;
    tag: string;
    key: string;
  }

  const operations = $derived.by<Op[]>(() => {
    if (!spec) return [];
    const out: Op[] = [];
    for (const [path, item] of Object.entries<Spec>(spec.paths)) {
      for (const method of METHODS) {
        if (!item[method]) continue;
        const op = item[method];
        out.push({ method, path, op, tag: op.tags?.[0] ?? 'Other', key: method + ' ' + path });
      }
    }
    return out;
  });

  const filtered = $derived(
    operations.filter((o) => {
      if (!q.trim()) return true;
      const s = (o.path + ' ' + (o.op.summary ?? '') + ' ' + o.tag).toLowerCase();
      return s.includes(q.toLowerCase());
    })
  );

  const tags = $derived.by<string[]>(() => {
    const order: string[] = (spec?.tags ?? []).map((t: Spec) => t.name);
    const present = new Set(filtered.map((o) => o.tag));
    const ordered = order.filter((t) => present.has(t));
    for (const t of present) if (!ordered.includes(t)) ordered.push(t);
    return ordered;
  });

  function opsFor(tag: string) {
    return filtered.filter((o) => o.tag === tag);
  }
  function secLabel(op: Spec) {
    const schemes = (op.security ?? []).flatMap((s: Spec) => Object.keys(s));
    if (!op.security) return 'API key';
    if (schemes.length === 0) return 'public';
    return schemes.join(' or ');
  }
</script>

<article class="max-w-none">
  {#if error}
    <p class="rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-2 text-sm text-rose-300">{error}</p>
  {:else if !spec}
    <div class="flex min-h-[30vh] items-center justify-center text-sm text-zinc-500">Loading reference...</div>
  {:else}
    <h1 class="text-2xl font-semibold tracking-tight">{spec.info.title} reference</h1>
    <p class="mt-2 max-w-2xl text-sm leading-relaxed text-zinc-400">{spec.info.description}</p>

    <div class="mt-4 flex flex-wrap items-center gap-3 text-xs text-zinc-500">
      <span>Auth:</span>
      <span><code class="font-mono text-zinc-400">Authorization: Bearer &lt;api-key&gt;</code> for management</span>
      <span>&middot;</span>
      <span><code class="font-mono text-zinc-400">?sentry_key=</code> / <code class="font-mono text-zinc-400">x-flare-key</code> for ingest</span>
      <a href="/openapi.yaml" class="ml-auto text-amber-400 hover:text-amber-300">Download OpenAPI &darr;</a>
    </div>

    <input
      bind:value={q}
      placeholder="Filter endpoints..."
      class="mt-5 w-full rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm outline-none focus:border-amber-400/60"
    />

    {#each tags as tag (tag)}
      <h2 class="mt-8 mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">{tag}</h2>
      <div class="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800/80">
        {#each opsFor(tag) as o (o.key)}
          <div>
            <button
              onclick={() => (open = { ...open, [o.key]: !open[o.key] })}
              class="flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors hover:bg-zinc-900/40"
            >
              <span class="w-14 shrink-0 rounded border px-1.5 py-0.5 text-center font-mono text-[10px] font-semibold uppercase {methodColor[o.method]}">{o.method}</span>
              <code class="truncate font-mono text-[13px] text-zinc-200">{o.path}</code>
              <span class="hidden truncate text-xs text-zinc-500 sm:inline">{o.op.summary}</span>
            </button>
            {#if open[o.key]}
              <div class="border-t border-zinc-800/60 bg-zinc-950/40 px-4 py-3 text-sm">
                {#if o.op.summary}<p class="text-zinc-300">{o.op.summary}</p>{/if}
                {#if o.op.description}<p class="mt-1 text-xs leading-relaxed text-zinc-500">{o.op.description}</p>{/if}
                <p class="mt-2 text-xs text-zinc-600">Auth: <span class="font-mono text-zinc-400">{secLabel(o.op)}</span></p>

                {#if o.op.parameters?.length}
                  <div class="mt-3">
                    <div class="mb-1 text-[11px] font-medium uppercase tracking-wide text-zinc-500">Parameters</div>
                    <ul class="space-y-1">
                      {#each o.op.parameters as p (p.name)}
                        <li class="flex items-baseline gap-2 text-xs">
                          <code class="font-mono text-zinc-300">{p.name}</code>
                          <span class="text-zinc-600">{p.in}{p.required ? ' · required' : ''}{p.schema?.type ? ' · ' + p.schema.type : ''}</span>
                          {#if p.description}<span class="text-zinc-500">{p.description}</span>{/if}
                        </li>
                      {/each}
                    </ul>
                  </div>
                {/if}

                {#if o.op.requestBody}
                  {@const body = Object.values(o.op.requestBody.content ?? {})[0] as Spec}
                  {@const reqd = (body?.schema?.required ?? []) as string[]}
                  <div class="mt-3">
                    <div class="mb-1 text-[11px] font-medium uppercase tracking-wide text-zinc-500">Request body</div>
                    {#if body?.schema?.properties}
                      <ul class="space-y-0.5">
                        {#each Object.entries<Spec>(body.schema.properties) as [name, prop] (name)}
                          <li class="flex items-baseline gap-2 text-xs">
                            <code class="font-mono text-zinc-300">{name}</code>
                            <span class="text-zinc-600">{prop.type ?? ''}{reqd.includes(name) ? ' · required' : ''}</span>
                            {#if prop.description}<span class="text-zinc-500">{prop.description}</span>{/if}
                          </li>
                        {/each}
                      </ul>
                    {/if}
                    {#if body?.example}
                      <pre class="mt-2 overflow-x-auto rounded border border-zinc-800 bg-zinc-950 p-2 font-mono text-[11px] text-zinc-400">{JSON.stringify(body.example, null, 2)}</pre>
                    {/if}
                  </div>
                {/if}

                <div class="mt-3">
                  <div class="mb-1 text-[11px] font-medium uppercase tracking-wide text-zinc-500">Responses</div>
                  <ul class="space-y-0.5">
                    {#each Object.entries<Spec>(o.op.responses) as [code, resp] (code)}
                      <li class="flex items-baseline gap-2 text-xs">
                        <code class="font-mono {code.startsWith('2') ? 'text-emerald-300' : 'text-zinc-400'}">{code}</code>
                        <span class="text-zinc-500">{resp.description}</span>
                      </li>
                    {/each}
                  </ul>
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/each}
  {/if}
</article>
