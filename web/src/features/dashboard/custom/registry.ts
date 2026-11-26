import { nanoid } from 'nanoid'

import type {
  BoardGranularity,
  BoardRange,
  BoardRefresh,
  BoardTime,
  DashboardPanel,
  PanelChart,
  PanelDimension,
  PanelMetric,
  PanelSource,
  PersonalDashboard,
} from './types'

export const SOURCE_LABELS: Record<PanelSource, string> = {
  usage: 'Model usage',
  sources: 'Call sources',
  live: 'Live gateway metrics',
}
export const METRIC_LABELS: Record<PanelMetric, string> = {
  requests: 'Requests',
  tokens: 'Total Tokens',
  cache_read: 'Cache Read',
  cache_write: 'Cache Write',
  quota: 'Revenue',
  upstream_cost: 'Upstream Cost',
  profit: 'Gross Profit',
  success_rate: 'Success rate',
  latency: 'Average latency',
  ttft: 'Average TTFT',
  tps: 'Throughput',
  p95: 'P95 latency',
  concurrency: 'Peak concurrency',
}
export const CHART_LABELS: Record<PanelChart, string> = {
  line: 'Line chart',
  area: 'Area chart',
  bar: 'Bar chart',
  pie: 'Distribution',
  stat: 'Value',
  table: 'Table',
}
export const DIMENSION_LABELS: Record<PanelDimension, string> = {
  model: 'Model',
  client: 'Client',
  total: 'Total',
}
export const RANGE_LABELS: Record<BoardRange, string> = {
  '15m': 'Last 15 minutes',
  '1h': 'Last hour',
  '6h': 'Last 6 hours',
  '24h': 'Last 24 hours',
  '7d': 'Last 7 days',
  '30d': 'Last 30 days',
  today: 'Today',
  yesterday: 'Yesterday',
}
export const GRANULARITY_LABELS: Record<BoardGranularity, string> = {
  auto: 'Auto',
  minute: '1m',
  '5m': '5m',
  hour: '1h',
  day: '1d',
}

export function getPanelCapabilities(
  source: PanelSource,
  isAdmin: boolean
): {
  metrics: PanelMetric[]
  dimensions: PanelDimension[]
  charts: PanelChart[]
  ranges: BoardRange[]
  granularities: BoardGranularity[]
  refreshIntervals: BoardRefresh[]
} {
  if (source === 'live') {
    return {
      metrics: isAdmin
        ? [
            'requests',
            'success_rate',
            'latency',
            'ttft',
            'tps',
            'p95',
            'concurrency',
          ]
        : [],
      dimensions: ['model', 'total'],
      charts: ['line', 'area', 'bar', 'stat', 'table'],
      ranges: ['15m', '1h', '6h', '24h'],
      granularities: ['auto', 'minute', '5m', 'hour'],
      refreshIntervals: [0, 5, 15, 30, 60],
    }
  }
  const metrics: PanelMetric[] = ['requests', 'tokens', 'quota']
  if (source === 'usage') metrics.push('cache_read', 'cache_write')
  if (isAdmin) metrics.push('upstream_cost', 'profit')
  let dimensions: PanelDimension[] = ['total']
  if (source === 'usage') dimensions = ['model', 'total']
  else if (isAdmin) dimensions = ['client', 'total']
  return {
    metrics,
    dimensions,
    charts: ['line', 'area', 'bar', 'pie', 'stat', 'table'],
    ranges: ['6h', '24h', '7d', '30d', 'today', 'yesterday'],
    granularities: ['auto', 'hour', 'day'],
    refreshIntervals: [0, 30, 60],
  }
}

export function isPanelAllowed(
  panel: DashboardPanel,
  isAdmin: boolean
): boolean {
  const capabilities = getPanelCapabilities(panel.source, isAdmin)
  return (
    capabilities.metrics.includes(panel.metric) &&
    capabilities.dimensions.includes(panel.dimension) &&
    capabilities.charts.includes(panel.chart) &&
    !(panel.metric === 'profit' && panel.chart === 'pie') &&
    !(panel.metric === 'concurrency' && panel.dimension !== 'total') &&
    !(panel.metric === 'concurrency' && panel.filters.model) &&
    !(panel.filters.username && (!isAdmin || panel.source === 'live')) &&
    !(panel.filters.model && panel.source === 'sources')
  )
}

export function resolvePanelTime(
  source: PanelSource,
  time: BoardTime
): BoardTime {
  const capabilities = getPanelCapabilities(source, true)
  const range = capabilities.ranges.includes(time.range) ? time.range : '24h'
  let granularity = capabilities.granularities.includes(time.granularity)
    ? time.granularity
    : 'auto'
  if (granularity === 'auto') {
    granularity = source === 'live' ? 'minute' : 'hour'
    if (source === 'live' && range === '24h') granularity = '5m'
    if (range === '30d') granularity = 'day'
  }
  const refresh =
    time.refresh === 0
      ? 0
      : (Math.max(source === 'live' ? 5 : 30, time.refresh) as BoardRefresh)
  return { range, granularity, refresh }
}

export function createDefaultPanel(
  source: PanelSource = 'usage'
): DashboardPanel {
  return {
    id: nanoid(12),
    title: '',
    source,
    metric: 'requests',
    chart: 'line',
    dimension: source === 'sources' ? 'client' : 'model',
    filters: {},
    layout: { x: 0, y: 0, w: 6, h: 5 },
  }
}

export function createDefaultDashboard(isAdmin: boolean): PersonalDashboard {
  const usage = createDefaultPanel()
  const tokens = {
    ...createDefaultPanel(),
    metric: 'tokens' as const,
    chart: 'area' as const,
    layout: { x: 6, y: 0, w: 6, h: 5 },
  }
  const panels = [usage, tokens]
  if (isAdmin) {
    panels.push({
      ...createDefaultPanel('live'),
      metric: 'latency',
      layout: { x: 0, y: 5, w: 6, h: 5 },
      time: { range: '1h', granularity: 'minute', refresh: 5 },
    })
    panels.push({
      ...createDefaultPanel('live'),
      metric: 'concurrency',
      dimension: 'total',
      layout: { x: 6, y: 5, w: 6, h: 5 },
      time: { range: '1h', granularity: 'minute', refresh: 5 },
    })
  }
  return {
    id: nanoid(12),
    name: '',
    time: { range: 'today', granularity: 'auto', refresh: 30 },
    panels,
  }
}
