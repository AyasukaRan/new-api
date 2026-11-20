/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Bar, CartesianGrid, ComposedChart, Line, XAxis, YAxis } from 'recharts'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { usePerfMetricsSummary } from '@/features/performance-metrics/hooks'
import {
  formatLatency,
  formatThroughput,
  formatUptimePct,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { MonitoredModelHotspots } from '../models/monitored-model-hotspots'
import { PanelWrapper } from '../ui/panel-wrapper'

type MonitorPeriod = { hours?: 24 | 168 }

export function PerformanceHealthPanel(
  props: MonitorPeriod & { timelineOnly?: boolean }
) {
  return (
    <GatewayMonitorPanel
      view='health'
      timelineOnly={props.timelineOnly}
      hours={props.hours}
    />
  )
}

export function ResponsePerformancePanel(props: MonitorPeriod) {
  return <GatewayMonitorPanel view='performance' hours={props.hours} />
}

export function GatewayModelHotspotsPanel(props: MonitorPeriod) {
  const { t } = useTranslation()
  const query = usePerfMetricsSummary({ diagnostics: true, hours: props.hours })
  const rows = query.data?.data.diagnostics?.models
  const title = t('Monitored model hotspots')
  return (
    <section className='min-w-0' aria-label={title} aria-busy={query.isLoading}>
      <PanelWrapper
        title={title}
        description={
          props.hours === 168
            ? t('Monitored gateway requests · last 7 days')
            : t('Monitored gateway requests · last 24 hours')
        }
        className='h-full rounded-xl shadow-none'
        contentClassName='px-3 py-2 sm:px-4 sm:py-2'
        loading={query.isLoading}
        empty={!query.isError && !rows}
        emptyMessage={t('Unavailable')}
      >
        {query.isError && (
          <ErrorState
            onRetry={() => void query.refetch()}
            className='min-h-44'
          />
        )}
        {!query.isError && rows && <MonitoredModelHotspots models={rows} />}
      </PanelWrapper>
    </section>
  )
}

function GatewayMonitorPanel(
  props: MonitorPeriod & {
    view: 'health' | 'performance'
    timelineOnly?: boolean
  }
) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = usePerfMetricsSummary({ diagnostics: true, hours: props.hours })
  const [metric, setMetric] = useState<'latency' | 'ttft' | 'throughput'>(
    'latency'
  )
  const [showModels, setShowModels] = useState(false)
  const health = props.view === 'health'
  const title = health ? t('Request health') : t('Response performance')
  const result = query.data?.data
  const diagnostics = result?.diagnostics
  const hours = diagnostics?.hours ?? []
  const points = hours.map((hour) => ({
    time: hour.ts,
    ttft: hour.avg_ttft_ms ?? null,
    success: hour.success_count,
    failed: hour.request_count - hour.success_count,
    latency:
      hour.request_count > 0 && hour.avg_latency_ms > 0
        ? hour.avg_latency_ms
        : null,
    throughput: hour.avg_tps > 0 ? hour.avg_tps : null,
  }))
  const totals = diagnostics?.total
  const successRate =
    totals && totals.request_count > 0
      ? (100 * totals.success_count) / totals.request_count
      : Number.NaN
  const config = {
    success: { label: t('Success'), color: 'var(--color-emerald-500)' },
    failed: { label: t('Failed'), color: 'var(--color-rose-500)' },
    ttft: { label: t('Average TTFT'), color: 'var(--color-violet-500)' },
    latency: { label: t('Average latency'), color: 'var(--color-blue-500)' },
    throughput: { label: t('Throughput'), color: 'var(--color-emerald-500)' },
  }
  const perfFormat =
    metric === 'throughput'
      ? formatThroughput
      : (value: number) => (value === 0 ? '0ms' : formatLatency(value))
  const perfTitle = {
    latency: t('Hourly average latency'),
    ttft: t('Hourly time to first token'),
    throughput: t('Hourly generation speed'),
  }[metric]
  const chartTitle = health ? t('Hourly request outcomes') : perfTitle
  const formatPerfTick = (value: number) =>
    value === 0 ? '0' : perfFormat(value)
  const unavailable = t('Unavailable')
  const kpis = health
    ? [
        {
          label: t('Requests'),
          value: totals
            ? formatNumber(totals.request_count, locale)
            : unavailable,
        },
        {
          label: t('Success rate'),
          value: formatUptimePct(successRate),
          tone: getSuccessRateTextClass(successRate),
        },
        {
          label: config.success.label,
          value: totals
            ? formatNumber(totals.success_count, locale)
            : unavailable,
          dot: config.success.color,
        },
        {
          label: config.failed.label,
          value: totals
            ? formatNumber(totals.request_count - totals.success_count, locale)
            : unavailable,
          dot: config.failed.color,
        },
      ]
    : [
        {
          label: config.latency.label,
          value: formatLatency(totals?.avg_latency_ms ?? 0),
          dot: config.latency.color,
        },
        {
          label: config.ttft.label,
          value:
            totals?.avg_ttft_ms === 0
              ? '0ms'
              : formatLatency(totals?.avg_ttft_ms ?? 0),
          dot: config.ttft.color,
        },
        {
          label: config.throughput.label,
          value: formatThroughput(totals?.avg_tps ?? 0),
          dot: config.throughput.color,
        },
      ]
  const hasSeries = health
    ? (totals?.request_count ?? 0) > 0
    : points.some((point) => point[metric] !== null)
  const tickTime = new Intl.DateTimeFormat(locale, {
    ...(props.hours === 168
      ? ({ month: 'numeric', day: 'numeric' } as const)
      : {}),
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  const hoverTime = new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })

  return (
    <section className='min-w-0' aria-label={title} aria-busy={query.isLoading}>
      <PanelWrapper
        title={title}
        description={
          props.hours === 168
            ? t('Monitored gateway requests · last 7 days')
            : t('Monitored gateway requests · last 24 hours')
        }
        className='h-full rounded-xl shadow-none'
        contentClassName='space-y-4 px-3 pb-3 sm:px-4 sm:pb-4'
        loading={query.isLoading}
        headerActions={
          !query.isError &&
          !props.timelineOnly &&
          (health ? (
            <ToggleGroup
              aria-label={title}
              size='sm'
              value={[showModels ? 'models' : 'timeline']}
              onValueChange={([value]) => {
                if (value === 'models' || value === 'timeline') {
                  setShowModels(value === 'models')
                }
              }}
            >
              <ToggleGroupItem value='timeline'>
                {t('Timeline')}
              </ToggleGroupItem>
              <ToggleGroupItem value='models'>{t('Models')}</ToggleGroupItem>
            </ToggleGroup>
          ) : (
            <ToggleGroup
              aria-label={title}
              size='sm'
              value={[metric]}
              onValueChange={([value]) => {
                if (
                  value === 'latency' ||
                  value === 'ttft' ||
                  value === 'throughput'
                ) {
                  setMetric(value)
                }
              }}
            >
              <ToggleGroupItem value='latency'>{t('Latency')}</ToggleGroupItem>
              <ToggleGroupItem value='ttft'>{t('First token')}</ToggleGroupItem>
              <ToggleGroupItem value='throughput'>
                {t('Throughput')}
              </ToggleGroupItem>
            </ToggleGroup>
          ))
        }
      >
        {query.isError ? (
          <ErrorState
            onRetry={() => void query.refetch()}
            className='min-h-44'
          />
        ) : (
          <>
            <dl
              className={cn(
                'bg-muted/55 grid gap-2 rounded-lg px-3 py-2.5',
                health ? 'grid-cols-2 sm:grid-cols-4' : 'grid-cols-3'
              )}
            >
              {kpis.map((kpi) => (
                <div key={kpi.label} className='min-w-0'>
                  <dt className='text-muted-foreground flex items-center gap-1.5 text-xs'>
                    {kpi.dot && (
                      <span
                        aria-hidden='true'
                        className='size-2 shrink-0 rounded-sm'
                        style={{ backgroundColor: kpi.dot }}
                      />
                    )}
                    {kpi.label}
                  </dt>
                  <dd
                    className={cn(
                      'mt-1 text-sm font-semibold tabular-nums',
                      kpi.tone
                    )}
                  >
                    {kpi.value}
                  </dd>
                </div>
              ))}
            </dl>
            {health &&
              showModels &&
              (diagnostics ? (
                <MonitoredModelHotspots models={diagnostics.models} />
              ) : (
                <EmptyState title={unavailable} className='min-h-44' />
              ))}
            {(!health || !showModels) &&
              (hasSeries ? (
                <figure aria-label={chartTitle}>
                  <ChartContainer
                    config={config}
                    className='aspect-auto h-44 w-full sm:h-48'
                  >
                    <ComposedChart
                      data={points}
                      title={chartTitle}
                      margin={{ top: 6, right: 8, bottom: 0, left: 0 }}
                      accessibilityLayer
                    >
                      <CartesianGrid vertical={false} strokeDasharray='3 4' />
                      <XAxis
                        dataKey='time'
                        tickFormatter={(v: number) => tickTime.format(v * 1000)}
                        minTickGap={22}
                        tickMargin={7}
                        tick={{ fill: 'var(--muted-foreground)' }}
                        tickLine={false}
                        axisLine={false}
                      />
                      <YAxis
                        width='auto'
                        tickCount={4}
                        allowDecimals={!health}
                        tick={{ fill: 'var(--muted-foreground)' }}
                        tickLine={false}
                        axisLine={false}
                        tickMargin={7}
                        tickFormatter={(v: number) =>
                          health
                            ? formatCompactNumber(v, locale)
                            : formatPerfTick(v)
                        }
                      />
                      <ChartTooltip
                        content={
                          <ChartTooltipContent
                            labelFormatter={(_, payload) =>
                              hoverTime.format(
                                Number(payload[0]?.payload.time) * 1000
                              )
                            }
                            formatter={(value, name) => (
                              <>
                                {name}
                                <span className='ml-auto pl-3 font-semibold tabular-nums'>
                                  {health
                                    ? formatNumber(Number(value), locale)
                                    : perfFormat(Number(value))}
                                </span>
                              </>
                            )}
                          />
                        }
                      />
                      {health ? (
                        <>
                          <Bar
                            dataKey='success'
                            name={config.success.label}
                            stackId='outcomes'
                            fill='var(--color-success)'
                            maxBarSize={20}
                            isAnimationActive={false}
                          />
                          <Bar
                            dataKey='failed'
                            name={config.failed.label}
                            stackId='outcomes'
                            fill='var(--color-failed)'
                            maxBarSize={20}
                            isAnimationActive={false}
                          />
                        </>
                      ) : (
                        <Line
                          type='linear'
                          dataKey={metric}
                          name={config[metric].label}
                          stroke={config[metric].color}
                          strokeWidth={2}
                          dot={{
                            r: 2,
                            strokeWidth: 0,
                            fill: config[metric].color,
                          }}
                          isAnimationActive={false}
                        />
                      )}
                    </ComposedChart>
                  </ChartContainer>
                </figure>
              ) : (
                <EmptyState
                  title={diagnostics ? t('No data available') : unavailable}
                  className='min-h-44'
                />
              ))}
          </>
        )}
      </PanelWrapper>
    </section>
  )
}
