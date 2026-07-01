<script lang="ts">
  // Public docs. No auth needed.
  const sentrySnippet = `import * as Sentry from '@sentry/node';

Sentry.init({
  dsn: 'https://<key>@your-flare-host/<project-id>',  // from your project page
  release: '1.4.2',
  environment: 'production'
});

// errors are captured automatically; or:
Sentry.captureException(new Error('payment gateway timeout'));`;

  const curlSnippet = `curl -X POST "https://your-flare-host/api/<project-id>/store/?sentry_key=<key>" \\
  -H 'Content-Type: application/json' \\
  -d '{
    "level": "error",
    "platform": "node",
    "release": "1.4.2",
    "exception": { "values": [{ "type": "Error", "value": "boom" }] }
  }'`;

  const otlpSnippet = `# Point any OpenTelemetry exporter at Flare:
OTEL_EXPORTER_OTLP_ENDPOINT=https://your-flare-host/otlp
OTEL_EXPORTER_OTLP_HEADERS=x-flare-key=<key>`;

  const sourcemapSnippet = `# In CI, after your build, upload each .map for the release:
curl -X POST "https://your-flare-host/api/<project-id>/artifacts" \\
  -H "Authorization: Bearer $FLARE_API_KEY" \\
  -H 'Content-Type: application/json' \\
  -d "$(jq -n --arg c "$(cat dist/app.min.js.map)" \\
        '{release:"1.4.2", name:"app.min.js", content:$c}')"`;

  const releaseSnippet = `# Mark a deploy from CI:
curl -X POST "https://your-flare-host/api/<project-id>/releases" \\
  -H "Authorization: Bearer $FLARE_API_KEY" \\
  -H 'Content-Type: application/json' \\
  -d '{"version": "1.4.2"}'`;
</script>

<article class="prose-flare max-w-2xl">
  <h1 class="text-2xl font-semibold tracking-tight">Flare docs</h1>
  <p class="mt-2 text-sm leading-relaxed text-zinc-400">
    Flare is sovereign, self-hostable observability: errors, logs and traces on one Postgres. Send data with any
    Sentry SDK (change only the DSN) or over OpenTelemetry. Manage everything with an org API key.
  </p>

  <div class="mt-6 grid grid-cols-1 gap-3 sm:grid-cols-3">
    {#each [['Errors', 'Grouped issues, symbolicated stack traces, regression + spike alerts'], ['Logs', 'Structured search + volume, DuckDB-backed'], ['Traces', 'Span waterfalls + latency percentiles']] as [h, d] (h)}
      <div class="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-4">
        <div class="text-sm font-medium text-zinc-200">{h}</div>
        <div class="mt-1 text-xs leading-relaxed text-zinc-500">{d}</div>
      </div>
    {/each}
  </div>

  <h2 id="send" class="mt-10 scroll-mt-20 text-lg font-semibold tracking-tight">Send your first event</h2>
  <p class="mt-1 text-sm text-zinc-400">
    Create a project in the dashboard to get a <strong>DSN</strong>. Then pick a path:
  </p>

  <h3 class="mt-5 text-sm font-medium text-zinc-300">A Sentry SDK (recommended)</h3>
  <p class="mt-1 text-sm text-zinc-500">Any <code class="font-mono text-zinc-400">@sentry/*</code> SDK works. Change only the DSN host.</p>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12px] leading-relaxed text-zinc-300">{sentrySnippet}</pre>

  <h3 class="mt-5 text-sm font-medium text-zinc-300">Or plain HTTP</h3>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12px] leading-relaxed text-zinc-300">{curlSnippet}</pre>

  <h3 class="mt-5 text-sm font-medium text-zinc-300">Logs & traces over OpenTelemetry</h3>
  <p class="mt-1 text-sm text-zinc-500">Point any OTLP exporter at Flare; the OTLP endpoint is on your project page.</p>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12px] leading-relaxed text-zinc-300">{otlpSnippet}</pre>

  <h2 id="sourcemaps" class="mt-10 scroll-mt-20 text-lg font-semibold tracking-tight">Readable stack traces (source maps)</h2>
  <p class="mt-1 text-sm text-zinc-400">
    Upload a <code class="font-mono text-zinc-400">.map</code> per release and minified frames resolve to your original
    source, with context. Do it from CI with an API key (create one in Settings):
  </p>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12px] leading-relaxed text-zinc-300">{sourcemapSnippet}</pre>

  <h2 id="releases" class="mt-10 scroll-mt-20 text-lg font-semibold tracking-tight">Track releases</h2>
  <p class="mt-1 text-sm text-zinc-400">
    Set <code class="font-mono text-zinc-400">release</code> on your events and Flare tracks it automatically. Mark a
    deploy from CI so it shows with its time, and every issue records the version it was first seen in:
  </p>
  <pre class="mt-2 overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12px] leading-relaxed text-zinc-300">{releaseSnippet}</pre>

  <h2 class="mt-10 text-lg font-semibold tracking-tight">Everything else</h2>
  <p class="mt-1 text-sm text-zinc-400">
    Issues, alerts (Slack/email/webhook), GitHub issue creation, team roles, SSO and more are in the
    <a href="/docs/api" class="text-amber-400 hover:text-amber-300">API reference</a>. Self-hosting? See the
    <a href="https://github.com/bright-interaction/flare/tree/main/deploy" class="text-amber-400 hover:text-amber-300">deploy guide</a>
    (Docker Compose + Helm).
  </p>
</article>
