<script lang="ts">
  // Hand-rolled inline-SVG sparkline (area + line). No charting library, per
  // the house no-CDN rule. Scales to its container via viewBox.
  let {
    values = [],
    height = 30,
    accent = '#fbbf24'
  }: { values?: number[]; height?: number; accent?: string } = $props();

  const W = 120;
  const max = $derived(Math.max(1, ...values));
  const pts = $derived(
    values.map((v, i) => {
      const x = values.length > 1 ? (i / (values.length - 1)) * W : 0;
      const y = height - 1 - (v / max) * (height - 2);
      return [x, y] as const;
    })
  );
  const line = $derived(pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' '));
  const area = $derived(pts.length ? `0,${height} ${line} ${W},${height}` : '');
</script>

{#if values.length}
  <svg
    viewBox={`0 0 ${W} ${height}`}
    preserveAspectRatio="none"
    class="h-full w-full"
    role="img"
    aria-label="activity over the last 24 hours"
  >
    <polygon points={area} fill={accent} fill-opacity="0.1" />
    <polyline
      points={line}
      fill="none"
      stroke={accent}
      stroke-width="1.5"
      vector-effect="non-scaling-stroke"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </svg>
{/if}
