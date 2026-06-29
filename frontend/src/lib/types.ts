export interface User {
  id: string;
  email: string;
  org_id: string;
  role: string;
}

export interface Project {
  id: string;
  name: string;
  slug: string;
  platform: string;
  dsn: string;
  otlp_endpoint: string;
}

export interface Issue {
  id: string;
  title: string;
  culprit: string;
  level: string;
  status: string;
  platform: string;
  first_seen: string;
  last_seen: string;
  event_count: number;
}

export interface Frame {
  filename?: string;
  function?: string;
  module?: string;
  lineno?: number;
  colno?: number;
  in_app?: boolean;
  context_line?: string;
}

export interface IssueEvent {
  id: string;
  level: string;
  message: string;
  exception_type: string;
  exception_value: string;
  platform: string;
  environment: string;
  release: string;
  stacktrace: { frames: Frame[] } | null;
  received_at: string;
}

export interface Channel {
  id: string;
  type: string;
  config: Record<string, unknown>;
  enabled: boolean;
}

export interface AlertRule {
  id: string;
  name: string;
  type: string;
  threshold: number;
  enabled: boolean;
}
