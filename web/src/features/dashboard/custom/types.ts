export type BoardRange =
  | '15m'
  | '1h'
  | '6h'
  | '24h'
  | '7d'
  | '30d'
  | 'today'
  | 'yesterday'
export type BoardGranularity = 'auto' | 'minute' | '5m' | 'hour' | 'day'
export type BoardRefresh = 0 | 5 | 15 | 30 | 60
export interface BoardTime {
  range: BoardRange
  granularity: BoardGranularity
  refresh: BoardRefresh
}
export type PanelSource = 'usage' | 'sources' | 'live'
export type PanelMetric =
  | 'requests'
  | 'tokens'
  | 'cache_read'
  | 'cache_write'
  | 'quota'
  | 'upstream_cost'
  | 'profit'
  | 'success_rate'
  | 'latency'
  | 'ttft'
  | 'tps'
  | 'p95'
  | 'concurrency'
export type PanelChart = 'line' | 'area' | 'bar' | 'pie' | 'stat' | 'table'
export type PanelDimension = 'model' | 'client' | 'total'
export interface DashboardPanel {
  id: string
  title: string
  source: PanelSource
  metric: PanelMetric
  chart: PanelChart
  dimension: PanelDimension
  filters: { model?: string; username?: string }
  time?: BoardTime
  layout: { x: number; y: number; w: number; h: number }
}
export interface PersonalDashboard {
  id: string
  name: string
  time: BoardTime
  panels: DashboardPanel[]
}
export interface DashboardConfig {
  version: 1
  active: string
  boards: PersonalDashboard[]
}
export interface DashboardConfigResponse {
  revision: number
  config: DashboardConfig | null
}
export interface LivePoint {
  ts: number
  model: string
  requests: number
  success_count: number
  latency_ms: number | null
  ttft_ms: number | null
  tps: number | null
  p95_ms: number | null
  concurrency: number | null
}
export interface LiveDashboardResponse {
  instance: string
  observed_from: number
  window_start: number
  window_end: number
  step_seconds: number
  active: number
  models_truncated: boolean
  p95_limited: boolean
  series: LivePoint[]
  totals: LivePoint[]
  summary: LivePoint[]
  total: LivePoint
}
