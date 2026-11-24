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
import { getLogCacheUsage } from '../lib/cache-usage'
import {
  formatFirstResponseTime,
  getFirstResponseTimeColor,
} from '../lib/format'
import { isDisplayableLogType, isTimingLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { CacheHitRing } from './cache-hit-rate'
import { LogBreakdownBar } from './log-breakdown-bar'
import { LogDurationPopover } from './log-duration-popover'
import { renderLogMetricTerm } from './log-metric-icons'
import { LogMetricPopover } from './log-metric-popover'

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
  const timing = isTimingLogType(log.type)
  if (!isDisplayableLogType(log.type) && !timing) return null

  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const formatCount = (value: number | null) =>
    value == null ? '—' : formatNumber(value, locale)
  const cache = getLogCacheUsage(log.prompt_tokens, other)
  const metered = other?.metered_usage
  const hasTokens =
    log.prompt_tokens > 0 ||
    log.completion_tokens > 0 ||
    cache.read != null ||
    cache.write != null ||
    other?.input_tokens_total != null ||
    other?.reasoning_tokens != null
  const tokenlessTask =
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
  const elapsed = hasMeasuredDuration ? durationMs / 1000 : log.use_time
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
  const tokenDetails = (
    <LogTokenBreakdown
      cache={cache}
      completion={log.completion_tokens}
      other={other}
      locale={locale}
    />
  )
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
          {other.metered_usage_estimated && (
            <span className='text-muted-foreground'>
              {t('Estimated usage')}
            </span>
          )}
        </div>
      ) : (
        !tokenlessTask && (
          <dl className={cn(metricGroup, 'relative')}>
            {renderLogMetricTerm('Total input tokens', t)}
            <dd className='text-emerald-700 dark:text-emerald-300'>
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
                    aria-label={
                      t('Cache read tokens') + ' · ' + t('Token Breakdown')
                    }
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

function LogTokenBreakdown({
  cache,
  completion,
  other,
  locale,
}: {
  cache: ReturnType<typeof getLogCacheUsage>
  completion: number
  other: LogOtherData | null
  locale?: string
}) {
  const { t } = useTranslation()
  const output =
    Number.isSafeInteger(completion) && completion >= 0 ? completion : null
  const reasoning = other?.reasoning_tokens
  const recordedReasoning =
    reasoning != null &&
    Number.isSafeInteger(reasoning) &&
    reasoning >= 0 &&
    output != null &&
    reasoning <= output
      ? reasoning
      : null
  const total =
    cache.completeTotal &&
    cache.total != null &&
    output != null &&
    Number.isSafeInteger(cache.total + output)
      ? cache.total + output
      : null
  const primary = [
    {
      key: 'input',
      label: t('Uncached input'),
      value: cache.uncached,
      color: 'bg-sky-500',
    },
    {
      key: 'output',
      label: t('Output Tokens'),
      value: output,
      color: 'bg-violet-500',
    },
    {
      key: 'read',
      label: t('Cache read tokens'),
      value: cache.read,
      color: 'bg-teal-500',
    },
    {
      key: 'write',
      label: t('Cache write tokens'),
      value: cache.write,
      color: 'bg-amber-500',
    },
  ]
  const segments = primary.flatMap((row) =>
    row.value == null ? [] : [{ ...row, value: row.value }]
  )
  const measuredTotal = segments.reduce((sum, row) => sum + row.value, 0)
  const showChart =
    total != null &&
    total > 0 &&
    measuredTotal > 0 &&
    measuredTotal <= total &&
    segments.every((row) => Number.isSafeInteger(row.value) && row.value >= 0)
  const percentages = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const writesByDuration = [
    { label: t('Cache Creation (5m)'), value: other?.cache_creation_tokens_5m },
    { label: t('Cache Creation (1h)'), value: other?.cache_creation_tokens_1h },
    {
      label: t('Cache Creation (12h)'),
      value: other?.cache_creation_tokens_12h,
    },
  ].filter(
    (row) =>
      row.value != null && Number.isSafeInteger(row.value) && row.value > 0
  )
  return (
    <>
      {showChart && (
        <LogBreakdownBar
          label={t('Token distribution')}
          total={total}
          segments={segments}
        />
      )}
      <dl className='grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-y-1.5 [&_dt]:pe-3'>
        {primary.map(({ key, label, value, color }) => (
          <div
            key={key}
            className='col-span-full grid grid-cols-subgrid items-baseline'
          >
            <dt className='text-muted-foreground flex items-center gap-2'>
              <span
                aria-hidden='true'
                className={cn(
                  'size-2 shrink-0 rounded-sm',
                  value == null ? 'border border-muted-foreground/40' : color
                )}
              />
              <span>{label}</span>
              {showChart && value != null && (
                <span className='ms-auto text-[10px] tabular-nums'>
                  {percentages.format(value / total)}
                </span>
              )}
            </dt>
            <dd
              className={cn(
                'text-end font-medium tabular-nums',
                value == null && 'text-muted-foreground font-normal'
              )}
            >
              {value == null ? t('Not recorded') : formatNumber(value, locale)}
            </dd>
          </div>
        ))}
        {writesByDuration.map(({ label, value }) => (
          <div
            key={label}
            className='text-muted-foreground col-span-full grid grid-cols-subgrid items-baseline text-[11px]'
          >
            <dt className='ms-1 border-s ps-3'>{label}</dt>
            <dd className='text-end tabular-nums'>
              {formatNumber(value, locale)}
            </dd>
          </div>
        ))}
        <div className='col-span-full grid grid-cols-subgrid items-baseline gap-y-1'>
          <dt className='text-muted-foreground'>
            {t('Reasoning tokens')}{' '}
            <span className='text-[10px]'>({t('Included in output')})</span>
            {recordedReasoning != null &&
              recordedReasoning > 0 &&
              output != null && (
                <div className='mt-1 flex items-center gap-2'>
                  <div className='grow'>
                    <LogBreakdownBar
                      label={
                        t('Reasoning tokens') + ' · ' + t('Included in output')
                      }
                      total={output}
                      segments={[
                        {
                          key: 'reasoning',
                          label: t('Reasoning tokens'),
                          value: recordedReasoning,
                          color: 'bg-violet-500',
                        },
                      ]}
                    />
                  </div>
                  <span className='text-muted-foreground text-[10px] tabular-nums'>
                    {percentages.format(recordedReasoning / output)}
                  </span>
                </div>
              )}
          </dt>
          <dd
            className={cn(
              'text-end font-medium tabular-nums',
              recordedReasoning == null && 'text-muted-foreground font-normal'
            )}
          >
            {recordedReasoning == null
              ? t('Not recorded')
              : formatNumber(recordedReasoning, locale)}
          </dd>
        </div>
        <div className='col-span-full mt-1 grid grid-cols-subgrid items-baseline border-t pt-2 font-semibold'>
          <dt>{t('Total tokens')}</dt>
          <dd className='text-end tabular-nums'>
            {total != null ? formatNumber(total, locale) : t('Not recorded')}
          </dd>
        </div>
      </dl>
    </>
  )
}
