<script lang="ts">
  import type { OverviewHourBucket } from '$lib/types';

  // 24h event-volume area chart, hand-rolled inline SVG. Hovering reads out the
  // hour + count for the nearest bucket via a vertical guide.
  let { buckets = [] }: { buckets?: OverviewHourBucket[] } = $props();

  const W = 760;
  const H = 150;
  const PAD_X = 6;
  const PAD_Y = 10;

  const max = $derived(Math.max(1, ...buckets.map((b) => b.count)));
  const pts = $derived(
    buckets.map((b, i) => {
      const x = buckets.length > 1 ? PAD_X + (i / (buckets.length - 1)) * (W - 2 * PAD_X) : PAD_X;
      const y = H - PAD_Y - (b.count / max) * (H - 2 * PAD_Y);
      return [x, y] as const;
    })
  );
  const line = $derived(pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' '));
  const area = $derived(pts.length ? `${PAD_X},${H} ${line} ${W - PAD_X},${H}` : '');

  let hover = $state<number | null>(null);

  function move(e: PointerEvent) {
    const svg = e.currentTarget as SVGSVGElement;
    const rect = svg.getBoundingClientRect();
    const frac = (e.clientX - rect.left) / rect.width;
    hover = Math.max(0, Math.min(buckets.length - 1, Math.round(frac * (buckets.length - 1))));
  }

  function hourLabel(iso: string): string {
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  const active = $derived(hover !== null ? buckets[hover] : null);
  const activePt = $derived(hover !== null ? pts[hover] : null);
</script>

<div class="relative">
  {#if active && activePt}
    <div
      class="pointer-events-none absolute -top-1 z-10 -translate-x-1/2 -translate-y-full whitespace-nowrap rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs shadow-lg"
      style={`left: ${(activePt[0] / W) * 100}%`}
    >
      <span class="font-mono text-zinc-100">{active.count.toLocaleString()}</span>
      <span class="text-zinc-500"> events - {hourLabel(active.hour)}</span>
    </div>
  {/if}

  <svg
    viewBox={`0 0 ${W} ${H}`}
    preserveAspectRatio="none"
    class="h-[150px] w-full touch-none"
    role="img"
    aria-label="event volume over the last 24 hours"
    onpointermove={move}
    onpointerleave={() => (hover = null)}
  >
    <line x1={PAD_X} y1={H - PAD_Y} x2={W - PAD_X} y2={H - PAD_Y} stroke="#3f3f46" stroke-width="1" />
    <polygon points={area} fill="#fbbf24" fill-opacity="0.09" />
    <polyline
      points={line}
      fill="none"
      stroke="#fbbf24"
      stroke-width="1.75"
      vector-effect="non-scaling-stroke"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
    {#if activePt}
      <line
        x1={activePt[0]}
        y1={PAD_Y}
        x2={activePt[0]}
        y2={H - PAD_Y}
        stroke="#52525b"
        stroke-width="1"
        stroke-dasharray="3 3"
      />
      <circle cx={activePt[0]} cy={activePt[1]} r="3" fill="#fbbf24" />
    {/if}
  </svg>

  <div class="mt-2 flex justify-between px-1 text-[11px] text-zinc-600">
    <span>{buckets.length ? hourLabel(buckets[0].hour) : '-24h'}</span>
    <span>now</span>
  </div>
</div>
