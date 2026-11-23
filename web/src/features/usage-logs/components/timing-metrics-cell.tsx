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
import { isConsumeLogType } from '../lib/utils'
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
  logType: number
  isStream: boolean
  metadata: LogOtherData | null
  showEndpoint: boolean
}

export function StreamModeCell({
  logType,
  isStream,
  metadata,
  showEndpoint,
}: StreamModeCellProps) {
  const { t } = useTranslation()
  const status = metadata?.stream_status
  const showStreamError = isStream && status?.status === 'error'
  const observed = metadata?.response_transport
  let transport: LogOtherData['response_transport']
  if (observed === 'ws' || observed === 'sse' || observed === 'http') {
    transport = observed
  } else if (metadata?.ws === true) {
    transport = 'ws'
  }

  let explanation: string | undefined
  const path = metadata?.request_path
  const firstToken = metadata?.frt
  const sawOutput =
    status?.response_status === 'completed' ||
    (!status?.response_status &&
      typeof firstToken === 'number' &&
      firstToken > 0)
  const normalEnd =
    status?.status === 'ok' &&
    !status.end_error &&
    !status.error_count &&
    (!status.end_reason ||
      status.end_reason === 'done' ||
      status.end_reason === 'eof')
  if (
    !transport &&
    !metadata?.is_task &&
    isStream &&
    isConsumeLogType(logType) &&
    typeof path === 'string' &&
    path.split('?', 1)[0] === '/v1/messages' &&
    normalEnd &&
    sawOutput
  ) {
    transport = 'sse'
    explanation = showEndpoint
      ? t('Identified from the Messages endpoint and recorded stream output.')
      : t('Identified from recorded stream output.')
  }

  const preparedSse =
    !metadata?.is_task &&
    !transport &&
    metadata?.prepared_response_transport === 'sse'
  let label: string
  if (metadata?.is_task) {
    label = metadata.task_sync ? t('Sync') : t('Async')
  } else if (transport) {
    label = transport.toUpperCase()
  } else if (preparedSse) {
    label = 'SSE'
    explanation = t(
      'The gateway prepared SSE headers. No response body was recorded, so delivery to the client could not be confirmed.'
    )
  } else if (isStream) {
    label = t('Protocol not recorded')
    explanation = t(
      'The log records streaming mode but does not identify the response protocol.'
    )
  } else {
    label = t('Non-streamed response')
  }
  const badge = (
    <StatusBadge
      label={label}
      copyable={false}
      className={cn(
        'h-5 justify-center rounded-md px-1.5 font-mono text-[11px] font-semibold',
        !metadata?.is_task &&
          transport === 'sse' &&
          'bg-emerald-100 text-emerald-800 dark:bg-emerald-400/15 dark:text-emerald-200',
        !metadata?.is_task &&
          transport === 'ws' &&
          'bg-sky-100 text-sky-800 dark:bg-sky-400/15 dark:text-sky-200',
        (metadata?.is_task || !transport || transport === 'http') &&
          'bg-muted text-muted-foreground',
        preparedSse &&
          'border-muted-foreground/55 border border-dashed bg-transparent'
      )}
    />
  )
  return (
    <div className='inline-flex max-w-full items-center gap-1'>
      {explanation ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                tabIndex={0}
                aria-description={explanation}
                className='focus-visible:ring-ring inline-flex min-w-0 cursor-help rounded-md focus-visible:ring-2 focus-visible:outline-none'
              />
            }
          >
            {badge}
          </TooltipTrigger>
          <TooltipContent role='tooltip'>{explanation}</TooltipContent>
        </Tooltip>
      ) : (
        badge
      )}
      {showStreamError && (
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger
              render={
                <span
                  tabIndex={0}
                  aria-label={t('Stream Status')}
                  className='focus-visible:ring-ring rounded-sm focus-visible:ring-2'
                />
              }
            >
              <CircleAlert aria-hidden className='text-destructive size-3' />
            </TooltipTrigger>
            <TooltipContent>
              <div className='space-y-0.5 text-xs'>
                <p>
                  {t('Stream Status')}: {t('Error')}
                </p>
                <p>{status?.end_reason || 'unknown'}</p>
                {(status?.error_count ?? 0) > 0 && (
                  <p>
                    {t('Soft Errors')}: {status?.error_count}
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
