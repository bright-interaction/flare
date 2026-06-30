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
  github_url: string;
}

export interface GithubConfig {
  configured: boolean;
  repo: string;
}

export interface Frame {
  filename?: string;
  function?: string;
  module?: string;
  lineno?: number;
  colno?: number;
  in_app?: boolean;
  context_line?: string;
  pre_context?: string[];
  post_context?: string[];
  symbolicated?: boolean;
}

export interface Artifact {
  id: string;
  release: string;
  name: string;
  size: number;
  created_at: string;
}

export interface Member {
  id: string;
  email: string;
  role: string;
  is_you: boolean;
  created_at: string;
}

export interface Invite {
  id: string;
  email: string;
  role: string;
  expires_at: string;
  created_at: string;
  accept_url?: string;
  emailed?: boolean;
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

export interface LogRow {
  id: string;
  severity: string;
  body: string;
  attributes: Record<string, unknown> | null;
  trace_id: string;
  span_id: string;
  observed_at: string;
}

export interface TraceSummary {
  trace_id: string;
  root_name: string;
  span_count: number;
  has_error: boolean;
  duration_ms: number;
  started: string;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
}

export interface ApiKeyCreated {
  id: string;
  name: string;
  prefix: string;
  key: string;
}

export interface Span {
  span_id: string;
  parent_span_id: string;
  name: string;
  kind: string;
  status: string;
  start_unix_ms: number;
  duration_ms: number;
  attributes: Record<string, unknown> | null;
}

export interface LogVolumeBucket {
  hour: string;
  severity: string;
  count: number;
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
  window_minutes: number;
  enabled: boolean;
}
