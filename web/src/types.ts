export interface Inbound {
  id: number
  remark: string
  listen: string
  port: number
  method: string
  server_psk?: string
  udp_enabled: boolean
  enabled: boolean
  created_at: number
  updated_at: number
  client_count: number
  enabled_client_count: number
  up_bytes: number
  down_bytes: number
  online_ips: number
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
  status: 'active' | 'disabled' | 'expired' | 'quota_exceeded'
  used_bytes: number
  remaining_bytes: number
  online_ips: string[]
  inbound_remark: string
  method: string
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

export interface DailyUsage {
  day: string
  up_bytes: number
  down_bytes: number
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
  system: SystemInfo
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

export interface Settings {
  public_host: string
  public_port: number
  subscription_enabled: boolean
  allow_private_targets: boolean
  reset_day: number
  timezone: string
}

export interface Admin {
  id: number
  username: string
  must_change_password: boolean
  created_at?: number
  last_login_at?: number
}

export interface InboundInput {
  remark: string
  listen: string
  port: number
  method: string
  server_psk: string
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
