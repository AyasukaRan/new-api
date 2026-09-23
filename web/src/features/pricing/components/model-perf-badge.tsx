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
import { memo, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import {
  formatLatency,
  formatThroughput,
  getSuccessRateDotClass,
} from '@/features/performance-metrics/lib/format'
import type { SuccessRatePoint } from '@/features/performance-metrics/types'
import { formatDateTimeStr } from '@/lib/format'
import { cn } from '@/lib/utils'

export type ModelPerfBadgeData = {
  window_start?: number
  window_end?: number
  current_available?: boolean
  current_observed_at?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_series?: SuccessRatePoint[]
  availability_rate?: number | null
  availability_series?: SuccessRatePoint[] | null
}

export interface ModelPerfBadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  perf: ModelPerfBadgeData | undefined
}

const STATUS_SLOTS = Array.from({ length: 24 }, (_, slot) => slot)

export function ModelCurrentAvailability(props: {
  available?: boolean | null
  observedAt?: number
}) {
  const { t } = useTranslation()
  const observedAt = props.observedAt ?? 0
  const hasObservation = Number.isFinite(observedAt) && observedAt > 0
  let label = t('Not monitored')
  let variant: StatusVariant = 'neutral'
  if (props.available === true) {
    label = t('Currently available')
    variant = 'success'
  } else if (props.available === false) {
    label = t('Currently unavailable')
    variant = 'danger'
  } else if (hasObservation) {
    label = t('Waiting for update')
  }
  let title = label
  if (hasObservation) {
    title = t('Last observed: {{time}}', {
      time: formatDateTimeStr(new Date(observedAt * 1000)),
    })
  }

  return (
    <StatusBadge
      role='status'
      aria-label={t('Current status')}
      label={label}
      variant={variant}
      type='text'
      copyable={false}
      className='text-xs'
      title={title}
    />
  )
}

function ModelRateMetric(props: {
  label: string
  rate?: number | null
  series?: SuccessRatePoint[] | null
  windowStart?: number
  hint: string
  samplesLabel: string
  emptyLabel: string
}) {
  const hasRate =
    props.rate != null &&
    Number.isFinite(props.rate) &&
    props.rate >= 0 &&
    props.rate <= 100
  const statusRates = useMemo(() => {
    const windowStart =
      props.windowStart ?? (Math.floor(Date.now() / 1000 / 3600) - 23) * 3600
    const ratesByHour = new Map<number, number>()
    for (const point of props.series ?? []) {
      ratesByHour.set(point.ts, point.success_rate)
    }
    return STATUS_SLOTS.map((slot) =>
      ratesByHour.get(windowStart + slot * 3600)
    )
  }, [props.series, props.windowStart])

  return (
    <div role='group' aria-label={props.label} className='min-w-0'>
      <dt
        title={props.hint}
        className='text-muted-foreground text-[11px] leading-4'
      >
        {props.label}
      </dt>
      <dd className='text-foreground mt-1 font-mono text-xs'>
        {hasRate ? `${props.rate?.toFixed(1)}%` : props.emptyLabel}
      </dd>
      <dd
        role='img'
        aria-label={props.samplesLabel}
        title={props.samplesLabel}
        className='mt-1 flex h-3 w-24 items-center gap-px'
      >
        {STATUS_SLOTS.map((slot) => {
          const rate = statusRates[slot]
          return (
            <span
              key={slot}
              aria-hidden
              className={cn(
                'h-full w-[3px] shrink-0 rounded-xs',
                rate != null &&
                  Number.isFinite(rate) &&
                  rate >= 0 &&
                  rate <= 100
                  ? getSuccessRateDotClass(rate)
                  : 'bg-muted-foreground/15'
              )}
            />
          )
        })}
      </dd>
    </div>
  )
}

export const ModelPerfBadge = memo(function ModelPerfBadge(
  props: ModelPerfBadgeProps
) {
  const { t } = useTranslation()
  const latencyText = formatLatency(props.perf?.avg_latency_ms ?? 0)
  const throughputText = formatThroughput(props.perf?.avg_tps ?? 0).replace(
    ' t/s',
    't/s'
  )

  return (
    <div
      aria-label={t('Performance metrics for the last 24 hours')}
      className={cn(
        'flex w-full min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-2',
        props.className
      )}
    >
      <div className='basis-full'>
        <ModelCurrentAvailability
          available={props.perf?.current_available}
          observedAt={props.perf?.current_observed_at}
        />
      </div>
      <dl className='flex min-w-0 items-start gap-4 text-xs tabular-nums'>
        <ModelRateMetric
          label={t('Availability (last 24h)')}
          rate={props.perf?.availability_rate}
          series={props.perf?.availability_series}
          windowStart={props.perf?.window_start}
          hint={t(
            'Real requests and health checks both contribute to availability history. Current availability uses recent results from enabled channels; one successful channel is enough.'
          )}
          samplesLabel={t(
            'Availability history; gray bars indicate missing data.'
          )}
          emptyLabel={t('Not monitored')}
        />
        <div title={t('Average latency')}>
          <dt className='text-muted-foreground text-[11px] leading-4'>
            {t('Latency short')}
          </dt>
          <dd className='mt-1 font-mono whitespace-nowrap'>
            {latencyText === '—' ? '—s' : latencyText}
          </dd>
        </div>
        <div title={t('Throughput')}>
          <dt className='text-muted-foreground text-[11px] leading-4'>
            {t('Throughput short')}
          </dt>
          <dd className='mt-1 font-mono whitespace-nowrap'>
            {throughputText === '—' ? '—t/s' : throughputText}
          </dd>
        </div>
      </dl>
      {props.children}
    </div>
  )
})
