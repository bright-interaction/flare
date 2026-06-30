import type {
  AlertRule,
  ApiKey,
  ApiKeyCreated,
  Artifact,
  AuditEntry,
  Channel,
  GithubConfig,
  Invite,
  Member,
  Release,
  Issue,
  IssueEvent,
  LogRow,
  LogVolumeBucket,
  Project,
  Span,
  TraceSummary,
  User
} from './types';

let csrfToken: string | null = null;

async function csrf(): Promise<string> {
  if (csrfToken !== null) return csrfToken;
  const res = await fetch('/api/csrf', { credentials: 'same-origin' });
  const data = await res.json();
  csrfToken = data.csrf_token ?? '';
  return csrfToken!;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const opts: RequestInit = { method, credentials: 'same-origin', headers };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  if (method !== 'GET') {
    headers['X-CSRF-Token'] = await csrf();
  }
  const res = await fetch('/api' + path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const e = await res.json();
      if (e.error) msg = e.error;
    } catch {
      /* keep statusText */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  me: () => req<User>('GET', '/auth/me'),
  login: (email: string, password: string) => req<User>('POST', '/auth/login', { email, password }),
  register: (email: string, password: string, org_name: string) =>
    req<User>('POST', '/auth/register', { email, password, org_name }),
  logout: () => req<void>('POST', '/auth/logout'),
  forgotPassword: (email: string) => req<void>('POST', '/auth/forgot-password', { email }),
  resetPassword: (token: string, password: string) =>
    req<void>('POST', '/auth/reset-password', { token, password }),
  acceptInvite: (token: string, password: string) =>
    req<User>('POST', '/auth/accept-invite', { token, password }),

  githubConfig: () => req<GithubConfig>('GET', '/integrations/github'),
  setGithubConfig: (repo: string, token: string) =>
    req<GithubConfig>('PUT', '/integrations/github', { repo, token }),
  deleteGithubConfig: () => req<void>('DELETE', '/integrations/github'),
  createGithubIssue: (issueId: string) =>
    req<{ github_url: string }>('POST', `/issues/${issueId}/github`),

  auditLog: () => req<AuditEntry[]>('GET', '/audit-log'),
  exportData: () => req<Record<string, unknown>>('GET', '/export'),
  deleteOrg: () => req<void>('DELETE', '/org'),

  members: () => req<Member[]>('GET', '/members'),
  updateMemberRole: (userId: string, role: string) =>
    req<void>('PATCH', `/members/${userId}`, { role }),
  removeMember: (userId: string) => req<void>('DELETE', `/members/${userId}`),
  invites: () => req<Invite[]>('GET', '/invites'),
  inviteMember: (email: string, role: string) => req<Invite>('POST', '/invites', { email, role }),
  revokeInvite: (inviteId: string) => req<void>('DELETE', `/invites/${inviteId}`),

  projects: () => req<Project[]>('GET', '/projects'),
  createProject: (name: string, platform: string) =>
    req<Project>('POST', '/projects', { name, platform }),
  project: (id: string) => req<Project>('GET', `/projects/${id}`),
  deleteProject: (id: string) => req<void>('DELETE', `/projects/${id}`),

  issues: (pid: string, status?: string) =>
    req<{ issues: Issue[]; total: number }>(
      'GET',
      `/projects/${pid}/issues${status ? `?status=${encodeURIComponent(status)}` : ''}`
    ),
  issue: (id: string) => req<Issue>('GET', `/issues/${id}`),
  issueEvents: (id: string) => req<IssueEvent[]>('GET', `/issues/${id}/events`),
  setIssueStatus: (id: string, status: string) => req<Issue>('PATCH', `/issues/${id}`, { status }),

  logs: (pid: string, opts: { q?: string; severity?: string } = {}) => {
    const p = new URLSearchParams();
    if (opts.q) p.set('q', opts.q);
    if (opts.severity) p.set('severity', opts.severity);
    const qs = p.toString();
    return req<LogRow[]>('GET', `/projects/${pid}/logs${qs ? `?${qs}` : ''}`);
  },

  logVolume: (pid: string, hours = 24) =>
    req<LogVolumeBucket[]>('GET', `/projects/${pid}/analytics/log-volume?hours=${hours}`),

  releases: (pid: string) => req<Release[]>('GET', `/projects/${pid}/releases`),
  createRelease: (pid: string, version: string) =>
    req<Release>('POST', `/projects/${pid}/releases`, { version }),

  traces: (pid: string) => req<TraceSummary[]>('GET', `/projects/${pid}/traces`),
  trace: (pid: string, traceID: string) => req<Span[]>('GET', `/projects/${pid}/traces/${traceID}`),

  apiKeys: () => req<ApiKey[]>('GET', '/keys'),
  createApiKey: (name: string) => req<ApiKeyCreated>('POST', '/keys', { name }),
  deleteApiKey: (id: string) => req<void>('DELETE', `/keys/${id}`),

  channels: () => req<Channel[]>('GET', '/channels'),
  createChannel: (type: string, config: Record<string, unknown>) =>
    req<Channel>('POST', '/channels', { type, config }),
  deleteChannel: (id: string) => req<void>('DELETE', `/channels/${id}`),

  alertRules: (pid: string) => req<AlertRule[]>('GET', `/projects/${pid}/alert-rules`),
  createAlertRule: (
    pid: string,
    rule: { name: string; type: string; threshold?: number; window_minutes?: number }
  ) => req<AlertRule>('POST', `/projects/${pid}/alert-rules`, rule),
  deleteAlertRule: (pid: string, ruleId: string) =>
    req<void>('DELETE', `/projects/${pid}/alert-rules/${ruleId}`),

  artifacts: (pid: string) => req<Artifact[]>('GET', `/projects/${pid}/artifacts`),
  uploadSourceMap: (pid: string, release: string, name: string, content: string) =>
    req<Artifact>('POST', `/projects/${pid}/artifacts`, { release, name, content }),
  deleteArtifact: (pid: string, artifactId: string) =>
    req<void>('DELETE', `/projects/${pid}/artifacts/${artifactId}`)
};
