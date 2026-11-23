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
import { CircleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  dotColorMap,
  StatusBadge,
  textColorMap,
  type StatusVariant,
} from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatUseTime } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  formatFirstResponseTime,
  getFirstResponseTimeColor,
  getResponseTimeColor,
} from '../lib/format'
import type { LogOtherData } from '../types'

/**
 * Softened fills for the full-height timing bar. The bar sits directly beside
 * dense numeric text, so the saturated `dotColorMap` tones (tuned for small
 * dots and badges) read as too high-contrast at that size; a translucent fill
 * keeps the status legible while matching the page's muted palette.
 */
const barColorMap: Record<StatusVariant, string> = {
  ...dotColorMap,
  success: 'bg-success/90',
  warning: 'bg-warning/80',
  danger: 'bg-destructive/80',
  neutral: 'bg-neutral/80',
}

interface TimingMetricsCellProps {
  useTimeSec: number
  completionTokens: number
  frtMs?: number
  isStream: boolean
  className?: string
  /**
   * `bar` (default) draws a full-height color segment beside the labels,
   * matching the dense desktop table. `dot` swaps that segment for small
   * status dots inline with each label, matching the lighter-weight status
   * indicator used elsewhere on the mobile card.
   */
  indicator?: 'bar' | 'dot'
  compact?: boolean
}

export function TimingMetricsCell(props: TimingMetricsCellProps) {
  const { t, i18n } = useTranslation()
  const indicator = props.indicator ?? 'bar'
  const metrics: { label: string; value: string; variant: StatusVariant }[] = []
  if (props.isStream) {
    const ms = props.frtMs
    const recorded = ms != null && ms > 0
    metrics.push({
      label: t('First token'),
      value: recorded ? formatFirstResponseTime(ms) : t('N/A'),
      variant: recorded ? getFirstResponseTimeColor(ms / 1000) : 'neutral',
    })
  }
  metrics.push({
    label: t('Duration'),
    value: formatUseTime(props.useTimeSec),
    variant: getResponseTimeColor(props.useTimeSec, props.completionTokens),
  })
  if (props.useTimeSec > 0 && props.completionTokens > 0) {
    const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
    const rate = Math.round(props.completionTokens / props.useTimeSec)
    metrics.push({
      label: t('Output speed'),
      value: `${formatNumber(rate, locale)} t/s`,
      variant: 'neutral',
    })
  }

  return (
    <div className={cn('flex items-stretch gap-2', props.className)}>
      {indicator === 'bar' && (
        <span
          aria-hidden
          className='flex w-1 shrink-0 flex-col overflow-hidden rounded-full'
        >
          {metrics.map((metric) => (
            <span
              key={metric.label}
              className={cn('flex-1', barColorMap[metric.variant])}
            />
          ))}
        </span>
      )}
      <div
        className={cn(
          'flex min-h-8 min-w-0 flex-col justify-center gap-0.5 text-xs leading-tight',
          props.compact &&
            'min-h-0 flex-row flex-wrap items-center gap-x-2.5 gap-y-1'
        )}
      >
        {metrics.map((metric) => (
          <div key={metric.label} className='flex items-baseline gap-1.5'>
            {indicator === 'dot' && (
              <span
                aria-hidden
                className={cn(
                  'size-1.5 shrink-0 rounded-full',
                  dotColorMap[metric.variant]
                )}
              />
            )}
            <span className='text-muted-foreground'>{metric.label}</span>
            <span
              className={cn(
                'whitespace-nowrap tabular-nums',
                textColorMap[metric.variant]
              )}
            >
              {metric.value}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

interface StreamModeCellProps {
  transport?: LogOtherData['response_transport']
  isStream: boolean
  /** Task logs are asynchronous jobs; stream vs non-stream does not apply. */
  isTask?: boolean
  /** The task request returned its result inline, so it is a synchronous call. */
  isSyncTask?: boolean
  streamStatus?: LogOtherData['stream_status']
  className?: string
}

export function StreamModeCell(props: StreamModeCellProps) {
  const { t } = useTranslation()
  const showStreamError =
    props.isStream && props.streamStatus && props.streamStatus.status !== 'ok'
  let streamLabel = props.isStream ? t('Stream') : t('Non-stream')
  if (props.transport) streamLabel = props.transport.toUpperCase()
  if (props.isTask) {
    streamLabel = props.isSyncTask ? t('Sync') : t('Async')
  }

  return (
    <div className={cn('inline-flex items-center gap-1', props.className)}>
      <StatusBadge
        label={streamLabel}
        variant={props.isStream ? 'info' : 'neutral'}
        copyable={false}
      />
      {showStreamError && (
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger
              render={<CircleAlert className='text-destructive size-3' />}
            />
            <TooltipContent>
              <div className='space-y-0.5 text-xs'>
                <p>
                  {t('Stream Status')}: {t('Error')}
                </p>
                <p>{props.streamStatus?.end_reason || 'unknown'}</p>
                {(props.streamStatus?.error_count ?? 0) > 0 && (
                  <p>
                    {t('Soft Errors')}: {props.streamStatus?.error_count}
                  </p>
                )}
              </div>
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      )}
    </div>
  )
}
