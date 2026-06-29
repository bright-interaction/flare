export function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '';
  const secs = Math.round((Date.now() - then) / 1000);
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}

// levelColor returns Tailwind text classes for an event/issue level.
export function levelColor(level: string): string {
  switch (level) {
    case 'fatal':
      return 'text-red-400';
    case 'error':
      return 'text-rose-400';
    case 'warning':
      return 'text-amber-400';
    case 'info':
      return 'text-sky-400';
    default:
      return 'text-zinc-400';
  }
}

export function statusColor(status: string): string {
  switch (status) {
    case 'resolved':
      return 'text-emerald-400 border-emerald-400/30 bg-emerald-400/10';
    case 'ignored':
      return 'text-zinc-400 border-zinc-500/30 bg-zinc-500/10';
    default:
      return 'text-amber-400 border-amber-400/30 bg-amber-400/10';
  }
}
