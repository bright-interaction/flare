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
  project_id: string;
  title: string;
  culprit: string;
  level: string;
  status: string;
  platform: string;
  first_seen: string;
  last_seen: string;
  event_count: number;
  github_url: string;
  first_release: string;
  ai_triage: string;
  sensitive: string;
}

export interface AiConfig {
  enabled: boolean;
  base_url: string;
  model: string;
  format: string;
  auto_triage: boolean;
  triage_daily_budget: number;
}

export interface Release {
  version: string;
  created_at: string;
  new_issues: number;
}

export interface GithubConfig {
  configured: boolean;
  repo: string;
}

export interface AuditEntry {
  action: string;
  target: string;
  actor: string;
  created_at: string;
}

export interface OidcConfig {
  enabled: boolean;
  issuer: string;
  client_id: string;
  default_role: string;
  redirect_uri: string;
  login_url: string;
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
  trace_id: string;
  span_id: string;
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

export type ApiKeyRole = 'viewer' | 'member';

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  role: ApiKeyRole;
  /** Email of the user who minted it. Empty for keys created before the
   *  creator column existed, which a password reset will NOT revoke. */
  created_by: string;
  created_at: string;
  last_used_at: string | null;
}

export interface ApiKeyCreated {
  id: string;
  name: string;
  prefix: string;
  role: ApiKeyRole;
  key: string;
}

export interface MetricName {
  name: string;
  kind: string;
  points: number;
  last_seen: string;
}

export interface MetricPoint {
  value: number;
  labels: Record<string, unknown> | null;
  observed_at: string;
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
  last_attempt_at: string | null;
  last_ok_at: string | null;
  last_error: string;
  /** Projects this channel is routed to. EMPTY MEANS ALL PROJECTS, which is
   *  both the default and the behaviour before routing existed. */
  project_ids: string[];
}

export interface Monitor {
  id: string;
  slug: string;
  name: string;
  interval_seconds: number;
  grace_seconds: number;
  last_ping_at: string | null;
  last_status: string;
  state: string; // new | ok | missing | failed
  checkin_url: string;
}

export interface AlertRule {
  id: string;
  name: string;
  type: string;
  threshold: number;
  window_minutes: number;
  enabled: boolean;
}

export interface OverviewHourBucket {
  hour: string;
  count: number;
}

export interface OverviewIssue {
  id: string;
  title: string;
  level: string;
  event_count: number;
  project_id: string;
  project_name: string;
}

export type ProjectHealth = 'healthy' | 'alert' | 'spike' | 'silent';

export interface OverviewProject {
  id: string;
  name: string;
  slug: string;
  events_24h: number;
  unresolved: number;
  status: ProjectHealth;
  /** Newest signal of any level (heartbeats included), unix seconds. 0 = no pulse in 7d. */
  last_seen_unix: number;
  volume: number[];
}

export interface Overview {
  events_24h: number;
  unresolved: number;
  new_today: number;
  volume: OverviewHourBucket[];
  top_issues: OverviewIssue[];
  projects: OverviewProject[];
  alerting_blind: boolean;
}

/** Body of a 202 from an erasure endpoint: the request was accepted but not
 *  fully carried out. Aged telemetry remains in the object store and only an
 *  operator can remove it, so this must be shown, not swallowed. */
export interface PartialErasure {
  status: 'partial';
  hot_tier: string;
  cold_tier: string;
  cold_tier_note: string;
}

/** A page of logs. next_before is absent when there are no older records, which
 *  is what lets the UI tell "end of data" from "this page happened to be short". */
export interface LogPage {
  logs: LogRow[];
  next_before?: string;
}
