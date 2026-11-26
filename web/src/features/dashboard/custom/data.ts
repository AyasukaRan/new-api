import { buildChartTimeDomain } from '@/features/dashboard/lib/charts'
import { getDashboardMetricValue } from '@/features/dashboard/lib/finance'
import type {
  QuotaDataItem,
  SourceQuotaDataItem,
} from '@/features/dashboard/types'
import dayjs from '@/lib/dayjs'

import type {
  BoardTime,
  DashboardPanel,
  LiveDashboardResponse,
  LivePoint,
  PanelMetric,
} from './types'

export interface PanelSeries {
  key: string
  label: string
  value: number | null
}
export interface PanelData {
  series: PanelSeries[]
  points: Array<{ time: number; [key: string]: number | null }>
  observedFrom?: number
  instance?: string
  active?: number
}

export function boardTimeRange(
  time: BoardTime,
  now = Date.now()
): { start_timestamp: number; end_timestamp: number } {
  const end = dayjs(now)
  if (time.range === 'today') {
    return {
      start_timestamp: end.startOf('day').unix(),
      end_timestamp: end.unix(),
    }
  }
  if (time.range === 'yesterday') {
    const yesterday = end.subtract(1, 'day')
    return {
      start_timestamp: yesterday.startOf('day').unix(),
      end_timestamp: yesterday.endOf('day').unix(),
    }
  }
  const seconds = {
    '15m': 900,
    '1h': 3600,
    '6h': 21600,
    '24h': 86400,
    '7d': 604800,
    '30d': 2592000,
  }[time.range]
  return { start_timestamp: end.unix() - seconds, end_timestamp: end.unix() }
}

function usageValue(row: QuotaDataItem, metric: PanelMetric): number | null {
  if (metric === 'tokens') {
    return Number.isFinite(row.token_used) ? (row.token_used ?? null) : null
  }
  if (metric === 'cache_read') {
    return Number.isFinite(row.cache_read_tokens)
      ? (row.cache_read_tokens ?? null)
      : null
  }
  if (metric === 'cache_write') {
    return Number.isFinite(row.cache_creation_tokens)
      ? (row.cache_creation_tokens ?? null)
      : null
  }
  if (metric === 'upstream_cost' || metric === 'profit') {
    // Do not present a partial ledger as the complete cost or profit.
    if (
      !row.finance ||
      row.finance.unrecorded_count > 0 ||
      row.finance.recorded_count < (row.count ?? 0)
    ) {
      return null
    }
    return getDashboardMetricValue(
      row,
      metric === 'upstream_cost' ? 'cost' : 'profit'
    )
  }
  return getDashboardMetricValue(row, metric === 'quota' ? 'quota' : 'requests')
}

export function buildUsagePanelData(
  rows: Array<QuotaDataItem | SourceQuotaDataItem>,
  panel: DashboardPanel,
  range: { start_timestamp: number; end_timestamp: number },
  time: BoardTime
): PanelData {
  const groups = new Map<
    string,
    { total: number | null; buckets: Map<number, number | null> }
  >()
  const domain = buildChartTimeDomain(
    range,
    time.granularity === 'day' ? 'day' : 'hour'
  )
  const buckets = domain?.buckets ?? []
  for (const row of rows) {
    if (
      panel.filters.model &&
      (!('model_name' in row) || row.model_name !== panel.filters.model)
    ) {
      continue
    }
    const timestamp = row.created_at
    if (
      timestamp === undefined ||
      timestamp < range.start_timestamp ||
      timestamp > range.end_timestamp
    ) {
      continue
    }
    let label = ''
    if (panel.dimension === 'model' && 'model_name' in row) {
      label = row.model_name ?? ''
    }
    if (panel.dimension === 'client' && 'client_tool' in row) {
      label = row.client_tool
    }
    let group = groups.get(label)
    if (!group) {
      group = { total: 0, buckets: new Map() }
      groups.set(label, group)
    }
    const value = usageValue(row as QuotaDataItem, panel.metric)
    group.total =
      group.total === null || value === null ? null : group.total + value
    const bucket = buckets.find(
      (item) => timestamp >= item.start && timestamp < item.end
    )
    if (!bucket) continue
    const previous = group.buckets.get(bucket.start) ?? 0
    const unknown =
      group.buckets.has(bucket.start) &&
      group.buckets.get(bucket.start) === null
    group.buckets.set(
      bucket.start,
      unknown || value === null ? null : previous + value
    )
  }
  const sorted = [...groups].sort(
    (a, b) =>
      (b[1].total ?? -Infinity) - (a[1].total ?? -Infinity) ||
      a[0].localeCompare(b[0])
  )
  const series = sorted.map(([label, group], i) => ({
    key: `s${i}`,
    label,
    value: group.total,
  }))
  const points = buckets.map((bucket) => {
    const point: PanelData['points'][number] = { time: bucket.start }
    sorted.forEach(([, group], i) => {
      point[`s${i}`] = group.buckets.has(bucket.start)
        ? (group.buckets.get(bucket.start) ?? null)
        : 0
    })
    return point
  })
  return { series, points }
}

function liveValue(point: LivePoint, metric: PanelMetric): number | null {
  switch (metric) {
    case 'requests':
      return point.requests
    case 'success_rate':
      return point.requests > 0
        ? (100 * point.success_count) / point.requests
        : null
    case 'latency':
      return point.latency_ms
    case 'ttft':
      return point.ttft_ms
    case 'p95':
      return point.p95_ms
    case 'tps':
      return point.tps
    case 'concurrency':
      return point.concurrency
    default:
      return null
  }
}

export function buildLivePanelData(
  result: LiveDashboardResponse,
  panel: DashboardPanel
): PanelData {
  const total = panel.dimension === 'total'
  const summaries = total ? [result.total] : result.summary
  const series = [...summaries]
    .sort((a, b) => b.requests - a.requests || a.model.localeCompare(b.model))
    .map((point, i) => ({
      key: `s${i}`,
      label: total ? '' : point.model,
      value: liveValue(point, panel.metric),
    }))
  const keys = new Map(series.map((item) => [item.label, item.key]))
  const points = new Map<number, PanelData['points'][number]>()
  for (const point of total ? result.totals : result.series) {
    const key = keys.get(total ? '' : point.model)
    if (!key) continue
    const row = points.get(point.ts) ?? { time: point.ts }
    row[key] = liveValue(point, panel.metric)
    points.set(point.ts, row)
  }
  return {
    series,
    points: [...points.values()].sort((a, b) => a.time - b.time),
    observedFrom: result.observed_from,
    instance: result.instance,
    active: result.active,
  }
}
