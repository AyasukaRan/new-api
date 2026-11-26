import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Area,
  Bar,
  CartesianGrid,
  Cell,
  ComposedChart,
  Line,
  Pie,
  PieChart,
  XAxis,
  YAxis,
} from 'recharts'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { getSourceQuotaData, getUserQuotaDates } from '@/features/dashboard/api'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getLiveDashboard } from './api'
import {
  boardTimeRange,
  buildLivePanelData,
  buildUsagePanelData,
  type PanelData,
} from './data'
import {
  GRANULARITY_LABELS,
  METRIC_LABELS,
  RANGE_LABELS,
  isPanelAllowed,
  resolvePanelTime,
} from './registry'
import type { BoardTime, DashboardPanel } from './types'

const COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
  'var(--color-sky-500)',
  'var(--color-orange-400)',
  'var(--color-teal-400)',
]

export function DashboardPanelView(props: {
  panel: DashboardPanel
  time: BoardTime
  isAdmin: boolean
  userId: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const panel = props.panel
  const requestedTime = panel.time ?? props.time
  const time = useMemo(
    () => resolvePanelTime(panel.source, requestedTime),
    [panel.source, requestedTime]
  )
  const allowed = isPanelAllowed(panel, props.isAdmin)
  // Source and effective query parameters, not chart style, identify cached data.
  const query = useQuery({
    queryKey: [
      'personal-dashboard-data',
      props.userId,
      props.isAdmin,
      panel.source,
      time.range,
      time.granularity,
      panel.filters.username ?? '',
      panel.source === 'live' ? (panel.filters.model ?? '') : '',
    ],
    enabled: allowed,
    staleTime: 3000,
    gcTime: 60_000,
    refetchInterval: time.refresh ? time.refresh * 1000 : false,
    refetchIntervalInBackground: false,
    meta: { errorToast: false },
    queryFn: async ({ signal }) => {
      const range = boardTimeRange(time)
      if (panel.source === 'live') {
        const seconds = { '15m': 900, '1h': 3600, '6h': 21600, '24h': 86400 }[
          time.range as '15m' | '1h' | '6h' | '24h'
        ]
        let step = 3600
        if (time.granularity === 'minute') step = 60
        if (time.granularity === '5m') step = 300
        const data = await getLiveDashboard(
          { seconds, step, model: panel.filters.model },
          signal
        )
        return { kind: 'live' as const, data, range }
      }
      const params = {
        ...range,
        ...(props.isAdmin && panel.filters.username
          ? { username: panel.filters.username }
          : {}),
      }
      if (panel.source === 'sources') {
        const response = requireServerSuccess(
          await getSourceQuotaData(
            { ...params, time_series: true },
            props.isAdmin
          )
        )
        return { kind: 'usage' as const, data: response.data ?? [], range }
      }
      const response = requireServerSuccess(
        await getUserQuotaDates(params, props.isAdmin)
      )
      return { kind: 'usage' as const, data: response.data ?? [], range }
    },
  })
  const data = useMemo<PanelData | undefined>(() => {
    if (!query.data) return undefined
    if (query.data.kind === 'live') {
      return buildLivePanelData(query.data.data, panel)
    }
    return buildUsagePanelData(query.data.data, panel, query.data.range, time)
  }, [query.data, panel, time])
  if (!allowed) {
    return <EmptyState title={t('Unavailable')} className='min-h-0 flex-1' />
  }
  if (query.isPending) return <Skeleton className='m-3 min-h-24 flex-1' />
  if (query.isError && !data) {
    return (
      <ErrorState
        onRetry={() => void query.refetch()}
        className='min-h-0 flex-1'
      />
    )
  }
  const label = (value: string) =>
    value || t(panel.dimension === 'total' ? 'Total' : 'Unknown')
  const metric = panel.metric
  const format = (
    value: number | null | undefined,
    compact = false
  ): string => {
    if (value == null || !Number.isFinite(value)) return '—'
    if (
      metric === 'quota' ||
      metric === 'upstream_cost' ||
      metric === 'profit'
    ) {
      return formatQuotaWithCurrency(value, { digitsLarge: 2, digitsSmall: 6 })
    }
    if (metric === 'latency' || metric === 'ttft' || metric === 'p95') {
      return value < 1000
        ? `${formatNumber(value, locale)}ms`
        : `${formatNumber(value / 1000, locale)}s`
    }
    if (metric === 'success_rate') return `${formatNumber(value, locale)}%`
    if (metric === 'tps') return `${formatNumber(value, locale)} t/s`
    return compact
      ? formatCompactNumber(value, locale)
      : formatNumber(value, locale)
  }
  const series = data?.series ?? []
  let visible = series.slice(0, 8)
  if (panel.chart === 'pie' && series.length > 8) {
    const others = series.slice(7)
    const value = others.some((item) => item.value === null)
      ? null
      : others.reduce((sum, item) => sum + (item.value ?? 0), 0)
    visible = [
      ...series.slice(0, 7),
      { key: 'others', label: t('Other'), value },
    ]
  }
  const config = Object.fromEntries(
    visible.map((item, i) => [
      item.key,
      { label: label(item.label), color: COLORS[i] },
    ])
  )
  const liveResult = query.data?.kind === 'live' ? query.data.data : undefined
  return (
    <div className='flex min-h-0 flex-1 flex-col gap-2 p-3'>
      {!series.length && (
        <EmptyState title={t('No data available')} className='min-h-0 flex-1' />
      )}
      {series.length > 0 && panel.chart === 'stat' && (
        <div className='grid min-h-0 flex-1 grid-cols-[repeat(auto-fit,minmax(100px,1fr))] items-center gap-3 overflow-auto'>
          {visible.map((item) => (
            <div key={item.key} className='min-w-0'>
              <div
                className='text-muted-foreground truncate text-xs'
                title={label(item.label)}
              >
                {label(item.label)}
              </div>
              <div
                className='mt-1 text-2xl font-semibold tabular-nums'
                title={format(item.value)}
              >
                {format(item.value, true)}
              </div>
            </div>
          ))}
        </div>
      )}
      {series.length > 0 && panel.chart === 'table' && (
        <div className='min-h-0 flex-1 overflow-auto'>
          <Table withContainer={false}>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Name')}</TableHead>
                <TableHead className='text-right'>
                  {t(METRIC_LABELS[metric])}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {series.map((item) => (
                <TableRow key={item.key} className='h-9'>
                  <TableCell
                    className='max-w-64 truncate'
                    title={label(item.label)}
                  >
                    {label(item.label)}
                  </TableCell>
                  <TableCell className='text-right'>
                    {format(item.value)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {series.length > 0 && !['stat', 'table'].includes(panel.chart) && (
        <ChartContainer
          config={config}
          className='aspect-auto min-h-0 w-full flex-1'
        >
          {panel.chart === 'pie' ? (
            <PieChart>
              <Pie
                data={visible
                  .filter((item) => item.value !== null && item.value > 0)
                  .map((item) => ({ ...item, name: label(item.label) }))}
                dataKey='value'
                nameKey='name'
                innerRadius='52%'
                outerRadius='82%'
                isAnimationActive={false}
              >
                {visible
                  .filter((item) => item.value !== null && item.value > 0)
                  .map((item) => (
                    <Cell key={item.key} fill={config[item.key].color} />
                  ))}
              </Pie>
              <ChartTooltip
                content={
                  <ChartTooltipContent
                    formatter={(value, name) => (
                      <span>
                        {name}:{' '}
                        {format(typeof value === 'number' ? value : null)}
                      </span>
                    )}
                  />
                }
              />
            </PieChart>
          ) : (
            <ComposedChart
              data={data?.points}
              margin={{ top: 6, left: 0, right: 8, bottom: 0 }}
            >
              <CartesianGrid vertical={false} />
              <XAxis
                dataKey='time'
                type='number'
                domain={['dataMin', 'dataMax']}
                tickFormatter={(value: number) =>
                  dayjs(value * 1000).format(
                    time.granularity === 'day' ? 'MM-DD' : 'HH:mm'
                  )
                }
                tickLine={false}
                axisLine={false}
                minTickGap={40}
              />
              <YAxis
                tickFormatter={(value: number) => format(value, true)}
                width={66}
                tickLine={false}
                axisLine={false}
              />
              <ChartTooltip
                content={
                  <ChartTooltipContent
                    labelFormatter={(value) =>
                      dayjs(Number(value) * 1000).format('MM-DD HH:mm')
                    }
                    formatter={(value, name) => (
                      <span className='flex w-full justify-between gap-4'>
                        <span>{name}</span>
                        <strong className='tabular-nums'>
                          {format(typeof value === 'number' ? value : null)}
                        </strong>
                      </span>
                    )}
                  />
                }
              />
              {visible.map((item) => {
                const common = {
                  dataKey: item.key,
                  name: label(item.label),
                  stroke: config[item.key].color,
                  fill: config[item.key].color,
                  isAnimationActive: false,
                }
                if (panel.chart === 'bar') {
                  return (
                    <Bar key={item.key} {...common} radius={[3, 3, 0, 0]} />
                  )
                }
                const curve =
                  metric === 'concurrency' ? 'stepAfter' : 'monotone'
                if (panel.chart === 'area') {
                  return (
                    <Area
                      key={item.key}
                      {...common}
                      type={curve}
                      fillOpacity={0.1}
                      strokeWidth={2}
                      connectNulls={false}
                    />
                  )
                }
                return (
                  <Line
                    key={item.key}
                    {...common}
                    type={curve}
                    dot={false}
                    strokeWidth={2}
                    connectNulls={false}
                  />
                )
              })}
            </ComposedChart>
          )}
        </ChartContainer>
      )}
      {series.length > 1 && !['stat', 'table'].includes(panel.chart) && (
        <div className='flex max-h-10 shrink-0 flex-wrap justify-center gap-x-3 gap-y-1 overflow-hidden text-[10px]'>
          {visible.map((item) => (
            <span key={item.key} className='flex min-w-0 items-center gap-1'>
              <span
                className='size-1.5 shrink-0 rounded-full'
                style={{ background: config[item.key].color }}
              />
              <span className='max-w-40 truncate' title={label(item.label)}>
                {label(item.label)}
              </span>
            </span>
          ))}
        </div>
      )}
      <div className='text-muted-foreground flex shrink-0 flex-wrap items-center justify-between gap-x-2 text-[10px]'>
        <span>
          {t(RANGE_LABELS[time.range])} · {GRANULARITY_LABELS[time.granularity]}
          {data?.instance && (
            <span
              title={`${data.instance} · ${dayjs((data.observedFrom ?? 0) * 1000).format('YYYY-MM-DD HH:mm:ss')}\n${t('Excludes client cancellations and invalid requests')}`}
            >
              {' '}
              · {t('This gateway instance')}
            </span>
          )}
        </span>
        <span>
          {query.isError
            ? t('Update failed')
            : dayjs(query.dataUpdatedAt).format('HH:mm:ss')}
        </span>
        {metric === 'concurrency' && liveResult && (
          <span>
            {t('Active requests')}: {formatNumber(liveResult.active, locale)}
          </span>
        )}
        {series.length > 8 && !['table', 'pie'].includes(panel.chart) && (
          <span>{t('Showing top {{count}} series', { count: 8 })}</span>
        )}
        {liveResult?.models_truncated && panel.dimension !== 'total' && (
          <span>{t('Model series limit reached')}</span>
        )}
        {liveResult?.p95_limited && metric === 'p95' && (
          <span>{t('P95 sample limit reached')}</span>
        )}
      </div>
    </div>
  )
}
