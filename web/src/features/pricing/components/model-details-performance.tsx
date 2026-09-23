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
import { useQuery } from '@tanstack/react-query'
import { HeartPulse, Timer } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames as tableStyles,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { getPerfMetrics } from '@/features/performance-metrics/api'
import {
  formatLatency,
  formatThroughput,
  formatUptimePct,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import type {
  PerformanceChannel,
  PerformanceSeriesPoint,
  SuccessRatePoint,
} from '@/features/performance-metrics/types'
import { useIsAdmin } from '@/hooks/use-admin'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import type { UptimeDayPoint } from '../lib/mock-stats'
import type { PricingModel } from '../types'
import { ModelAdminChannelAvailability } from './model-admin-channel-availability'
import { LatencyTrendChart, UptimeTrendChart } from './model-details-charts'
import { UptimeSparkline } from './model-details-uptime-sparkline'
import { ModelCurrentAvailability } from './model-perf-badge'

function StatCard(props: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  value: React.ReactNode
  hint?: string
  valueClassName?: string
}) {
  const Icon = props.icon
  return (
    <div
      role='group'
      aria-label={props.label}
      className='bg-background flex flex-col gap-1 rounded-lg border p-3'
    >
      <span className='text-muted-foreground inline-flex items-center gap-1.5 text-[10px] font-medium tracking-wider uppercase'>
        <Icon className='size-3' />
        {props.label}
      </span>
      <span
        className={cn(
          'text-foreground font-mono text-lg font-semibold tabular-nums',
          props.valueClassName
        )}
      >
        {props.value}
      </span>
      {props.hint && (
        <span className='text-muted-foreground/70 text-[11px]'>
          {props.hint}
        </span>
      )}
    </div>
  )
}

function toUptimePct(value: number): number {
  if (!Number.isFinite(value)) return 0
  const clamped = Math.min(100, Math.max(0, value))
  return Math.round(clamped * 100) / 100
}

function toLatencySeries(series: PerformanceSeriesPoint[]) {
  return series
    .filter((point) => point.avg_ttft_ms > 0)
    .map((point) => ({
      timestamp: new Date(point.ts * 1000).toISOString(),
      group: 'latency',
      ttft_ms: point.avg_ttft_ms,
    }))
}

function toUptimeSeries(series?: SuccessRatePoint[] | null): UptimeDayPoint[] {
  return (series ?? [])
    .filter((point) => Number.isFinite(point.success_rate))
    .map((point) => ({
      date: new Date(point.ts * 1000).toISOString(),
      uptime_pct: toUptimePct(point.success_rate),
      incidents: point.success_rate < 100 ? 1 : 0,
      outage_minutes: 0,
    }))
}

export function ModelDetailsPerformance(props: { model: PricingModel }) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const metricsQuery = useQuery({
    queryKey: ['perf-metrics', props.model.model_name],
    queryFn: async () =>
      requireServerSuccess(await getPerfMetrics(props.model.model_name, 24)),
    staleTime: 60 * 1000,
    refetchInterval: 60 * 1000,
  })
  const groups = useMemo(
    () => metricsQuery.data?.data.groups ?? [],
    [metricsQuery.data]
  )
  const series = useMemo(
    () => metricsQuery.data?.data.series ?? [],
    [metricsQuery.data]
  )
  const latencySeries = useMemo(() => toLatencySeries(series), [series])
  const availabilitySeries = metricsQuery.data?.data.availability_series
  const channels = metricsQuery.data?.data.channels ?? []
  const availabilityRate =
    metricsQuery.data?.data.availability_rate ?? Number.NaN

  const observedAt = metricsQuery.data?.data.current_observed_at ?? 0
  const avgTps =
    metricsQuery.data?.data.summary?.avg_tps ??
    metricsQuery.data?.data.avg_tps ??
    0
  const avgLatency =
    metricsQuery.data?.data.summary?.avg_latency_ms ??
    metricsQuery.data?.data.avg_latency_ms ??
    0

  if (metricsQuery.isLoading) {
    return <LoadingState />
  }
  if (metricsQuery.isError || metricsQuery.data?.success === false) {
    return <ErrorState onRetry={() => void metricsQuery.refetch()} />
  }
  if (
    groups.length === 0 &&
    channels.length === 0 &&
    !availabilitySeries?.length &&
    metricsQuery.data?.data.current_available == null &&
    !(Number.isFinite(observedAt) && observedAt > 0) &&
    !(avgLatency > 0) &&
    !(avgTps > 0) &&
    !isAdmin
  ) {
    return (
      <EmptyState
        title={t('Not monitored')}
        description={t('Performance data is not yet available for this model.')}
        bordered
      />
    )
  }

  return (
    <div className='flex flex-col gap-4'>
      <ModelCurrentAvailability
        available={metricsQuery.data?.data.current_available}
        observedAt={metricsQuery.data?.data.current_observed_at}
      />
      <div className='grid grid-cols-1 gap-2 sm:grid-cols-3'>
        <StatCard
          icon={Timer}
          label='TPS'
          value={formatThroughput(avgTps)}
          hint={t('Sustained tokens per second')}
        />
        <StatCard
          icon={Timer}
          label={t('Average latency')}
          value={formatLatency(avgLatency)}
        />
        <StatCard
          icon={HeartPulse}
          label={t('Availability (last 24h)')}
          value={
            Number.isFinite(availabilityRate)
              ? formatUptimePct(availabilityRate)
              : t('Not monitored')
          }
          valueClassName={getSuccessRateTextClass(availabilityRate)}
        />
      </div>

      <ModelAvailabilityTimeline series={availabilitySeries} />

      <ModelChannelAvailability
        modelName={props.model.model_name}
        channels={channels}
      />

      <section>
        <SectionHeader
          icon={Timer}
          title={t('Latency trend (last 24h)')}
          description={t('Average TTFT')}
        />
        <LatencyTrendChart series={latencySeries} />
      </section>
    </div>
  )
}

export function ModelAvailabilityTimeline(props: {
  series?: SuccessRatePoint[] | null
}) {
  const { t } = useTranslation()
  const uptimeSeries = useMemo(
    () => toUptimeSeries(props.series),
    [props.series]
  )
  return (
    <section aria-label={t('Availability (last 24h)')}>
      <SectionHeader
        icon={HeartPulse}
        title={t('Availability (last 24h)')}
        description={t(
          'Real requests and health checks both contribute to availability history. Current availability uses recent results from enabled channels; one successful channel is enough.'
        )}
      />
      <UptimeTrendChart series={uptimeSeries} />
    </section>
  )
}

export function ModelChannelAvailability(props: {
  modelName: string
  channels: PerformanceChannel[]
}) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  return (
    <section aria-label={t('Channel availability')}>
      <SectionHeader
        icon={HeartPulse}
        title={t('Channel availability')}
        description={t(
          'Channel availability history includes real requests and health checks from the last 24 hours.'
        )}
      />
      {isAdmin ? (
        <ModelAdminChannelAvailability
          key={`${user?.id}:${user?.role}`}
          modelName={props.modelName}
        />
      ) : (
        <StaticDataTable
          className='rounded-lg'
          tableClassName='text-sm'
          tableProps={{ 'aria-label': t('Channel availability') }}
          headerRowClassName={tableStyles.compactHeaderRow}
          data={props.channels}
          getRowKey={(channel) => channel.channel_index}
          emptyContent={t('Not monitored')}
          columns={[
            {
              id: 'channel',
              header: t('Channel'),
              className: tableStyles.compactHeaderCell,
              cellClassName: tableStyles.compactCell,
              cell: (channel) =>
                t('Channel {{number}}', { number: channel.channel_index }),
            },
            {
              id: 'current-status',
              header: t('Current status'),
              className: tableStyles.compactHeaderCell,
              cellClassName: tableStyles.compactCell,
              cell: (channel) => (
                <ModelCurrentAvailability
                  available={channel.current_available}
                  observedAt={channel.current_observed_at}
                />
              ),
            },
            {
              id: 'availability',
              header: t('Availability (last 24h)'),
              className: tableStyles.compactHeaderCellRight,
              cellClassName: tableStyles.compactNumericCell,
              cell: (channel) => (
                <span
                  className={getSuccessRateTextClass(
                    channel.availability_rate ?? Number.NaN
                  )}
                >
                  {channel.availability_rate != null
                    ? formatUptimePct(channel.availability_rate)
                    : t('Not monitored')}
                </span>
              ),
            },
            {
              id: 'latency',
              header: t('Average latency'),
              className: tableStyles.compactHeaderCellRight,
              cellClassName: tableStyles.compactMutedNumericCell,
              cell: (channel) => formatLatency(channel.avg_latency_ms),
            },
            {
              id: 'trend',
              header: t('Availability (last 24h)'),
              className: cn(tableStyles.compactHeaderCell, 'min-w-36'),
              cellClassName: tableStyles.compactCell,
              cell: (channel) => (
                <UptimeSparkline
                  series={toUptimeSeries(channel.series)}
                  size='sm'
                  showOverall={false}
                  emptyLabel={t('Not monitored')}
                  ariaLabel={t('Channel {{number}} availability samples', {
                    number: channel.channel_index,
                  })}
                />
              ),
            },
          ]}
        />
      )}
    </section>
  )
}

function SectionHeader(props: {
  icon: React.ComponentType<{ className?: string }>
  title: string
  description?: string
  accent?: React.ReactNode
}) {
  const Icon = props.icon
  return (
    <div className='mb-2 flex flex-wrap items-center justify-between gap-2'>
      <div className='flex min-w-0 items-center gap-2'>
        <Icon className='text-muted-foreground/70 size-3.5 shrink-0' />
        <div className='min-w-0'>
          <div className='text-foreground text-sm font-semibold'>
            {props.title}
          </div>
          {props.description && (
            <p className='text-muted-foreground/80 text-xs'>
              {props.description}
            </p>
          )}
        </div>
      </div>
      {props.accent && (
        <div className='shrink-0 text-xs font-medium'>{props.accent}</div>
      )}
    </div>
  )
}
