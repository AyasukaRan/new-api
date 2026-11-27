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
import { useTranslation } from 'react-i18next'

import { textColorMap } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { getLogTokenTotals } from '../lib/cache-usage'
import {
  formatFirstResponseTime,
  getFirstResponseTimeColor,
} from '../lib/format'
import { getLongContextBilling } from '../lib/long-context-billing'
import { isDisplayableLogType, isTimingLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { CacheHitRing } from './cache-hit-rate'
import { LogDurationPopover } from './log-duration-popover'
import { renderLogMetricTerm } from './log-metric-icons'
import { LogMetricPopover } from './log-metric-popover'
import { LogTokenBreakdown } from './log-token-breakdown'
import { LongContextBillingTag } from './long-context-billing-tag'

const metricGroup =
  'grid auto-rows-[1.15rem] grid-cols-[max-content_max-content] content-center items-center justify-start gap-x-[5px] [&>dt]:text-muted-foreground [&>dd]:justify-self-start [&>dd]:text-start [&>dd]:font-mono [&>dd]:text-xs [&>dd]:font-semibold'

export function TokenPerformanceCell(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin?: boolean
  compact?: boolean
}) {
  const { t, i18n } = useTranslation()
  const { log, other } = props
  const timing = log.in_progress || isTimingLogType(log.type)
  if (!isDisplayableLogType(log.type) && !timing) return null

  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const formatCount = (value: number | null) =>
    value == null ? '—' : formatNumber(value, locale)
  const totals = getLogTokenTotals(log, other)
  const cache = log.in_progress
    ? { total: null, read: null, write: null, percent: null }
    : totals.cache
  const hasTokens = !log.in_progress && totals.hasUsage
  const longContext = log.in_progress ? null : getLongContextBilling(other)
  const metered = log.in_progress ? undefined : other?.metered_usage
  const tokenlessTask =
    !log.in_progress &&
    (other?.is_task === true || metered != null) &&
    !(
      log.prompt_tokens > 0 ||
      log.completion_tokens > 0 ||
      (cache.read ?? 0) > 0 ||
      (cache.write ?? 0) > 0 ||
      (cache.total ?? 0) > 0
    )
  const durationMs = other?.duration_ms
  const hasMeasuredDuration =
    durationMs != null && Number.isFinite(durationMs) && durationMs >= 0
  const measuredElapsed = hasMeasuredDuration ? durationMs / 1000 : log.use_time
  const elapsed = log.in_progress ? -1 : measuredElapsed
  const first =
    log.is_stream &&
    other?.frt != null &&
    Number.isFinite(other.frt) &&
    (other.frt > 0 || (other.frt === 0 && hasMeasuredDuration))
      ? other.frt
      : null
  const speed =
    elapsed > 0 && log.completion_tokens > 0
      ? Math.round(log.completion_tokens / elapsed)
      : null
  const tokenDetails = <LogTokenBreakdown log={log} other={other} />
  const tokenTriggerClass =
    'decoration-muted-foreground/65 h-auto border-0 p-0 font-mono text-xs font-semibold text-inherit underline decoration-dotted underline-offset-4 after:absolute after:inset-0'
  return (
    <div
      className={cn(
        'grid items-stretch gap-x-1.5 whitespace-nowrap text-[11px] leading-tight tabular-nums',
        props.compact
          ? 'w-full gap-y-1.5 [grid-template-columns:repeat(auto-fit,minmax(min(100%,6.25rem),1fr))]'
          : 'mx-auto w-max max-w-full grid-cols-[minmax(5.5rem,max-content)_minmax(6.25rem,max-content)_minmax(5.5rem,max-content)]'
      )}
    >
      {metered ? (
        <div className='flex flex-col justify-center'>
          <span className='font-mono text-xs font-semibold'>
            {formatCount(metered.count)} {t(metered.unit)}
          </span>
          {other?.metered_usage_estimated && (
            <span className='text-muted-foreground'>
              {t('Estimated usage')}
            </span>
          )}
        </div>
      ) : (
        !tokenlessTask && (
          <dl className={cn(metricGroup, 'relative')}>
            {renderLogMetricTerm('Total input tokens', t)}
            <dd className='inline-flex items-center gap-0.5 text-emerald-700 dark:text-emerald-300'>
              {hasTokens ? (
                <LogMetricPopover
                  label={t('Token Breakdown')}
                  trigger={
                    <Button
                      variant='link'
                      size='xs'
                      aria-label={t('Token Breakdown')}
                      className={tokenTriggerClass}
                    >
                      {formatCount(cache.total)}
                    </Button>
                  }
                >
                  {tokenDetails}
                </LogMetricPopover>
              ) : (
                '—'
              )}
              {longContext && (
                <span className='relative z-10 inline-flex'>
                  <LongContextBillingTag billing={longContext} />
                </span>
              )}
            </dd>
            {renderLogMetricTerm('Output Tokens', t)}
            <dd className='text-blue-700 dark:text-blue-300'>
              {hasTokens ? formatCount(log.completion_tokens) : '—'}
            </dd>
          </dl>
        )
      )}
      {!metered && !tokenlessTask && (
        <dl className={cn(metricGroup, 'relative border-s ps-2.5')}>
          {renderLogMetricTerm('Cache read tokens', t)}
          <dd>
            {hasTokens ? (
              <LogMetricPopover
                label={t('Token Breakdown')}
                trigger={
                  <Button
                    variant='link'
                    size='xs'
                    aria-label={`${t('Cache read tokens')} · ${t('Token Breakdown')}`}
                    className={tokenTriggerClass}
                  >
                    {formatCount(cache.read)}
                  </Button>
                }
              >
                {tokenDetails}
              </LogMetricPopover>
            ) : (
              '—'
            )}
          </dd>
          {renderLogMetricTerm('Cache write tokens', t)}
          <dd>{formatCount(cache.write)}</dd>
          {renderLogMetricTerm('Cache hit rate', t)}
          <dd>
            <CacheHitRing percent={cache.percent} />
          </dd>
        </dl>
      )}
      {timing && (
        <dl
          className={cn(
            metricGroup,
            'border-s ps-2.5',
            (metered || tokenlessTask) && !props.compact && 'col-start-3',
            tokenlessTask && !metered && 'border-0 ps-0'
          )}
        >
          {(!tokenlessTask || first != null) && (
            <>
              {renderLogMetricTerm('Time to first token', t)}
              <dd
                className={
                  first != null
                    ? textColorMap[getFirstResponseTimeColor(first / 1000)]
                    : 'text-muted-foreground'
                }
              >
                {first != null ? formatFirstResponseTime(first) : '—'}
              </dd>
            </>
          )}
          {renderLogMetricTerm('Total time', t)}
          <dd>
            {elapsed >= 0 ? (
              <LogDurationPopover
                log={log}
                other={other}
                isAdmin={props.isAdmin === true}
              />
            ) : (
              '—'
            )}
          </dd>
          {!tokenlessTask && (
            <>
              {renderLogMetricTerm('Output speed', t)}
              <dd className='text-sky-700 dark:text-sky-300'>
                {speed != null ? `${formatCount(speed)} t/s` : '—'}
              </dd>
            </>
          )}
        </dl>
      )}
    </div>
  )
}
