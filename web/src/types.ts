export interface Inbound {
  id: number
  remark: string
  listen: string
  port: number
  method: string
  server_psk?: string
  /** Optional hostname for this inbound's share links (empty = use global). */
  domain: string
  /** Port for the domain, 0 = fall back to public_port then the inbound port. */
  domain_port: number
  udp_enabled: boolean
  enabled: boolean
  created_at: number
  updated_at: number
  client_count: number
  enabled_client_count: number
  up_bytes: number
  down_bytes: number
  online_ips: number
  /** Set when the row is enabled but the listener could not start. */
  last_error?: string
}

/**
 * ClientSummary is what the client list endpoints return: the fields the list
 * view renders. It carries no credentials or share link, so a bulk response
 * holds no secret and can safely be compressed.
 */
export interface ClientSummary {
  id: number
  inbound_id: number
  name: string
  enabled: boolean
  quota_bytes: number
  up_bytes: number
  down_bytes: number
  expires_at: number
  ip_limit: number
  reset_policy: 'none' | 'monthly'
  reset_day: number
  note: string
  /**
   * The computed status of the row. It is never the list-only filter values
   * `expiring` and `idle`: those exist only to narrow the list.
   */
  status: 'active' | 'disabled' | 'expired' | 'quota_exceeded'
  used_bytes: number
  remaining_bytes: number
  online_ips: string[]
  inbound_remark: string
  method: string
}

/**
 * ClientListParams is the query of the paged client list endpoints.
 *
 * Filtering and paging run in SQL: `limit` and `offset` select the slice, and
 * the optional fields narrow the row set before it is counted.
 */
export interface ClientListParams {
  inbound_id?: number
  /** The four computed statuses plus the two list-only filter values. */
  status?: ClientSummary['status'] | 'expiring' | 'idle' 
  q?: string
  limit: number
  offset: number
}

/** One page of the client list plus the paging values the server applied. */
export interface ClientListResponse {
  items: ClientSummary[]
  total: number
  limit: number
  offset: number
}

/**
 * Client is the single-client representation (GET /clients/:id): the summary
 * plus the credentials and the rendered share links.
 */
export interface Client extends ClientSummary {
  psk?: string
  sub_token: string
  last_reset_at: number
  created_at: number
  updated_at: number
  link: string
  subscription_url: string
}

/** The share link and subscription path returned by GET /clients/:id/link. */
export interface ClientLinkResponse {
  link: string
  subscription_url: string
}

/** The new token returned after rotating a client's subscription token. */
export interface ResetTokenResponse {
  sub_token: string
  subscription_url: string
}

export interface DailyUsage {
  day: string
  up_bytes: number
  down_bytes: number
}

/** One usage window returned by GET /clients/:id/usage. */
export interface UsageResponse {
  items: DailyUsage[]
}

export interface Dashboard {
  version: string
  uptime_seconds: number
  inbounds: number
  clients: number
  enabled_inbounds: number
  enabled_clients: number
  online_ips: number
  sessions: number
  up_bytes: number
  down_bytes: number
  traffic_30d: DailyUsage[]
  attention: Attention
  system: SystemInfo
}

/**
 * Attention is the dashboard's "needs attention" summary. Each number links to
 * the client list filtered to the same rows.
 */
export interface Attention {
  expiring_soon: number
  expired: number
  quota_exceeded: number
  idle: number
  expiring_window_days: number
}

export interface SystemInfo {
  os: string
  arch: string
  num_cpu: number
  goroutines: number
  cpu_percent: number
  mem_used: number
  mem_total: number
  heap_alloc: number
  host_metrics: boolean
}

/** The host/runtime snapshot returned by GET /system. */
export interface SystemResponse {
  version: string
  uptime_seconds: number
  system: SystemInfo
  sessions: number
}

export interface Settings {
  public_host: string
  public_port: number
  subscription_enabled: boolean
  allow_private_targets: boolean
  reset_day: number
  timezone: string
  /** Whether the data plane records proxy destinations. Off by default. */
  access_log_enabled: boolean
}

export interface Admin {
  id: number
  username: string
  must_change_password: boolean
  /** Whether a second factor is required at login. */
  totp_enabled: boolean
  created_at?: number
  last_login_at?: number
}

/** The login response carries the session expiry as unix seconds. */
export interface LoginResponse {
  admin: Admin
  expires_at: number
  totp_required?: false
}

/**
 * The first step of a 2FA login: no session yet. The challenge is the opaque
 * handle the second step submits together with the code; expires_at is unix
 * seconds after which the server rejects it.
 */
export interface TOTPChallengeResponse {
  totp_required: true
  challenge: string
  expires_at: number
}

/** POST /auth/login answers either a session or a second-factor challenge. */
export type LoginResult = LoginResponse | TOTPChallengeResponse

/** POST /auth/totp/setup: the secret to add to an authenticator app. */
export interface TOTPSetupResponse {
  secret: string
  otpauth_url: string
}

/** POST /auth/totp/enable: one-time recovery codes and the new state. */
export interface TOTPEnableResponse {
  recovery_codes: string[]
  totp_enabled: boolean
}

/** POST /auth/totp/recovery-codes: a freshly rotated set of recovery codes. */
export interface TOTPRecoveryCodesResponse {
  recovery_codes: string[]
}

/** POST /auth/totp/disable. */
export interface TOTPDisableResponse {
  ok: boolean
  totp_enabled: boolean
}

/** GET /auth/me returns the same admin shape as the login response. */
export type MeResponse = Admin

/** GET /inbounds: the rows plus the encryption methods the form offers. */
export interface InboundsResponse {
  items: Inbound[]
  total: number
  methods: string[]
}

export interface InboundInput {
  remark: string
  listen: string
  port: number
  method: string
  server_psk: string
  domain: string
  domain_port: number
  udp_enabled: boolean
  enabled: boolean
}

export interface ClientInput {
  name: string
  psk: string
  enabled: boolean
  quota_bytes: number
  expires_at: number
  ip_limit: number
  reset_policy: 'none' | 'monthly'
  reset_day: number
  note: string
}

/** One audit trail entry. */
export interface AuditLog {
  id: number
  created_at: number
  admin_id: number
  username: string
  action: string
  target_type: string
  target_id: number
  detail: string
  ip: string
}

export interface AuditResponse {
  items: AuditLog[]
  total: number
  limit: number
  offset: number
  actions: string[]
}

export interface ImportFailure {
  line: number
  name: string
  error: string
}

export interface ImportResult {
  created: number
  failed: ImportFailure[]
}

/** One row of the most requested destinations (GET /access/summary). */
export interface AccessTopHost {
  host: string
  hits: number
  blocked_hits: number
  up_bytes: number
  down_bytes: number
}

/** One row of the most active clients (GET /access/summary). */
export interface AccessTopClient {
  client_id: number
  name: string
  inbound_id: number
  inbound_remark: string
  hits: number
  up_bytes: number
  down_bytes: number
}

/** One day of the access trend. */
export interface AccessDaily {
  day: string
  hits: number
  allowed_hits: number
  blocked_hits: number
  failed_hits: number
  up_bytes: number
  down_bytes: number
}

/** The overview returned by GET /access/summary. */
export interface AccessSummary {
  enabled: boolean
  days: number
  raw_retention_days: number
  aggregate_retention_days: number
  dropped_records: number
  total_hits: number
  allowed_hits: number
  blocked_hits: number
  failed_hits: number
  unique_hosts: number
  up_bytes: number
  down_bytes: number
  daily: AccessDaily[]
  top_hosts: AccessTopHost[]
  top_clients: AccessTopClient[]
}

/** One stored access record. */
export interface AccessEvent {
  id: number
  started_at: number
  duration_ms: number
  client_id: number
  /** Empty when the user has been deleted; the view renders a fallback. */
  client_name: string
  inbound_id: number
  inbound_remark: string
  network: 'tcp' | 'udp'
  host: string
  port: number
  /** The address actually dialed; empty when the request was blocked. */
  ip: string
  outcome: 'allowed' | 'blocked' | 'failed'
  reason: string
  up_bytes: number
  down_bytes: number
}

export interface AccessEventsResponse {
  items: AccessEvent[]
  total: number
  limit: number
  offset: number
}

/** The query of GET /access/events; empty fields are left out. */
export interface AccessEventParams {
  client_id?: number
  inbound_id?: number
  outcome?: string
  network?: string
  q?: string
  /** Absolute instant bounds (unix seconds); the day form below takes precedence. */
  since?: number
  until?: number
  /**
   * Inclusive local days (YYYY-MM-DD), resolved by the server in the configured
   * time zone. The picker picks days, and only the server knows which zone they
   * are in, so the day form is what the page sends.
   */
  since_day?: string
  until_day?: string
  limit: number
  offset: number
}

/** One bulk action the panel can apply to a selection of users. */
export type BatchAction = 'enable' | 'disable' | 'delete' | 'extend' | 'add_quota' | 'reset'

/** The body of POST /batch/clients. */
export interface BatchRequest {
  action: BatchAction
  ids: number[]
  /** Used by "extend" only. */
  days?: number
  /** Used by "add_quota" only. */
  bytes?: number
}

/** One id a batch left alone, with the reason. */
export interface BatchFailure {
  id: number
  name: string
  error: string
}

/** The reply of POST /batch/clients: partial success, like the import. */
export interface BatchResult {
  action: BatchAction
  applied: number
  failed: BatchFailure[]
}

/** One day of an inbound's traffic series. */
export interface InboundDaily {
  day: string
  up_bytes: number
  down_bytes: number
}

/** One row of an inbound's busiest users. */
export interface InboundTopClient {
  id: number
  name: string
  up_bytes: number
  down_bytes: number
  enabled: boolean
}

/** The status distribution of one inbound's users. */
export interface InboundStatusCounts {
  active: number
  disabled: number
  expired: number
  quota_exceeded: number
}

/** The reply of GET /inbounds/:id/detail. */
export interface InboundDetail {
  inbound: Inbound
  days: number
  daily: InboundDaily[]
  top_clients: InboundTopClient[]
  status_counts: InboundStatusCounts
  recent_failures: AccessEvent[]
  /** False means the failure list is empty because nothing is recorded. */
  access_recording: boolean
}
