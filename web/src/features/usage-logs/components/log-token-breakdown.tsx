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

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { getLogTokenTotals } from '../lib/cache-usage'
import type { LogOtherData } from '../types'
import { CacheHitRing } from './cache-hit-rate'
import { LogBreakdownBar } from './log-breakdown-bar'

function isTokenCount(value: number | null | undefined): value is number {
  return value != null && Number.isSafeInteger(value) && value >= 0
}

export function LogTokenBreakdown(props: {
  log: Pick<UsageLog, 'prompt_tokens' | 'completion_tokens'>
  other: LogOtherData | null
  variant?: 'popover' | 'detail'
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { log, other } = props
  const detail = props.variant === 'detail'
  const { cache, output, total, hasUsage } = getLogTokenTotals(log, other)
  const reasoning = other?.reasoning_tokens
  const recordedReasoning =
    isTokenCount(reasoning) && output != null && reasoning <= output
      ? reasoning
      : null
  const primary = [
    {
      key: 'input',
      label: t('Uncached input'),
      value: cache.uncached,
      color: 'bg-sky-500',
      stroke: 'stroke-sky-500',
    },
    {
      key: 'output',
      label: t('Output Tokens'),
      value: output,
      color: 'bg-violet-500',
      stroke: 'stroke-violet-500',
    },
    {
      key: 'read',
      label: t('Cache read tokens'),
      value: cache.read,
      color: 'bg-teal-500',
      stroke: 'stroke-teal-500',
    },
    {
      key: 'write',
      label: t('Cache write tokens'),
      value: cache.write,
      color: 'bg-amber-500',
      stroke: 'stroke-amber-500',
    },
  ]
  const segments = primary.flatMap((row) =>
    row.value == null ? [] : [{ ...row, value: row.value }]
  )
  const measuredTotal = segments.reduce((sum, row) => sum + row.value, 0)
  const consistentShares =
    total != null &&
    total > 0 &&
    measuredTotal <= total &&
    segments.every((row) => isTokenCount(row.value))
  const showChart = consistentShares && measuredTotal > 0
  const percentages = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const writesByDuration = [
    { label: t('Cache Creation (5m)'), value: other?.cache_creation_tokens_5m },
    { label: t('Cache Creation (1h)'), value: other?.cache_creation_tokens_1h },
    {
      label: t('Cache Write (30m)'),
      value:
        detail &&
        (other?.cache_creation_tokens_12h ?? 0) > 0 &&
        !other?.cache_creation_tokens_5m &&
        !other?.cache_creation_tokens_1h &&
        cache.write != null
          ? cache.write - (other?.cache_creation_tokens_12h ?? 0)
          : undefined,
    },
    {
      label: t('Cache Creation (12h)'),
      value: other?.cache_creation_tokens_12h,
    },
  ].filter(
    (row) =>
      row.value != null && Number.isSafeInteger(row.value) && row.value > 0
  )
  const remaining = consistentShares ? total - measuredTotal : 0
  const legend =
    detail && remaining > 0
      ? [
          ...primary,
          {
            key: 'unknown',
            label: t('Unknown'),
            value: remaining,
            color: 'bg-muted-foreground/25',
          },
        ]
      : primary
  const text = total == null ? '—' : formatNumber(total, locale)
  const details = (
    <dl className='grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-baseline gap-y-1.5 [&_dt]:pe-3'>
      {legend.map(({ key, label, value, color }) => (
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
            {consistentShares && value != null && (
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
          {recordedReasoning != null && output != null && output > 0 && (
            <div className='mt-1 flex items-center gap-2'>
              <div className='grow'>
                <LogBreakdownBar
                  label={`${t('Reasoning tokens')} · ${t('Included in output')}`}
                  total={output}
                  segments={[
                    {
                      key: 'reasoning',
                      label: t('Reasoning tokens'),
                      value: recordedReasoning,
                      color: 'bg-violet-500',
                    },
                    {
                      key: 'other-output',
                      label: t('Non-reasoning output'),
                      value: output - recordedReasoning,
                      color: 'bg-muted-foreground/20',
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
  )
  if (!detail) {
    return (
      <>
        {showChart && (
          <LogBreakdownBar
            label={t('Token distribution')}
            total={total}
            segments={segments}
          />
        )}
        {details}
      </>
    )
  }

  const input =
    hasUsage && cache.completeTotal && isTokenCount(cache.total)
      ? cache.total
      : null
  const hitRate =
    input != null &&
    cache.percent != null &&
    isTokenCount(cache.read) &&
    isTokenCount(cache.write ?? 0) &&
    cache.read + (cache.write ?? 0) <= input ? (
      <CacheHitRing percent={cache.percent} />
    ) : null
  const analysis = [
    {
      label: t('Total input tokens'),
      value: input == null ? null : formatNumber(input, locale),
    },
    { label: t('Input cache hit rate'), value: hitRate },
  ]
  if (output != null && recordedReasoning != null) {
    analysis.push({
      label: t('Non-reasoning output'),
      value: formatNumber(output - recordedReasoning, locale),
    })
  }
  // Modality and cached-modality counts overlap the slices above.
  const cached = other?.cached_tokens_details
  const modalities = [
    {
      label: t('Text'),
      counts: [other?.text_input, cached?.text_tokens, other?.text_output],
    },
    {
      label: t('Audio'),
      counts: [
        other?.audio_input ?? other?.audio_input_token_count,
        cached?.audio_tokens,
        other?.audio_output,
      ],
    },
    {
      label: t('Image'),
      counts: [
        other?.image ? other.image_output : undefined,
        cached?.image_tokens ?? other?.image_cache_tokens,
        undefined,
      ],
    },
  ].filter((row) => row.counts.some(isTokenCount))
  const modalityColumns = [t('Input'), t('Cache Read'), t('Output')]
  // The neutral track is visible for unclassified input and for unavailable splits.
  let offset = 0
  const arcs = showChart
    ? segments
        .filter((segment) => segment.value > 0)
        .map((segment) => {
          const start = offset
          const percent = (segment.value / total) * 100
          offset += percent
          return { ...segment, start, percent }
        })
    : []
  return (
    <div className='@container space-y-4'>
      <div className='grid items-center gap-4 @min-[21rem]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.5fr)]'>
        <div className='relative mx-auto grid aspect-square w-32 max-w-full place-items-center @min-[28rem]:w-36'>
          <svg
            role='img'
            aria-label={t('Token distribution')}
            viewBox='0 0 120 120'
            className='absolute inset-0 size-full -rotate-90'
            fill='none'
            strokeWidth='10'
          >
            <circle
              cx='60'
              cy='60'
              r='51'
              className='stroke-muted-foreground/25'
            />
            {arcs.map((arc) => (
              <circle
                key={arc.key}
                className={arc.stroke}
                cx='60'
                cy='60'
                r='51'
                pathLength='100'
                strokeDasharray={`${arc.percent} 100`}
                strokeDashoffset={-arc.start}
              >
                <title>{arc.label}</title>
              </circle>
            ))}
          </svg>
          <div className='pointer-events-none flex max-w-[72%] flex-col items-center text-center'>
            <span
              className='text-foreground font-mono text-xl font-semibold tracking-tight tabular-nums'
              title={text}
            >
              {total == null || total < 1_000_000
                ? text
                : formatCompactNumber(total, locale)}
            </span>
            <span className='text-muted-foreground mt-0.5 text-[10px] leading-3'>
              {t('Total tokens')}
            </span>
          </div>
        </div>
        {details}
      </div>
      {hasUsage && (
        <dl className='grid grid-cols-2 gap-x-3 gap-y-2 border-t pt-3 @min-[24rem]:auto-cols-fr @min-[24rem]:grid-flow-col @min-[24rem]:grid-cols-none'>
          {analysis.map(({ label, value }) => (
            <div key={label} className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>{label}</dt>
              <dd
                className={cn(
                  'font-mono font-semibold tabular-nums',
                  value == null &&
                    'text-muted-foreground font-sans text-xs font-normal'
                )}
              >
                {value ?? t('Not recorded')}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {modalities.length > 0 && (
        <Table uniformCellText={false} className='text-xs'>
          <caption className='caption-top pb-1 text-start font-medium'>
            {t('Tokens by modality')}
          </caption>
          <TableHeader>
            <TableRow>
              <TableHead scope='col' className='h-7 ps-0 text-xs'>
                {t('Modality')}
              </TableHead>
              {modalityColumns.map((label) => (
                <TableHead
                  key={label}
                  scope='col'
                  className='text-muted-foreground h-7 text-end text-xs'
                >
                  {label}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody className='[&>tr]:h-7'>
            {modalities.map(({ label, counts }) => (
              <TableRow key={label}>
                <TableHead scope='row' className='h-7 ps-0 text-xs'>
                  {label}
                </TableHead>
                {counts.map((count, index) => (
                  <TableCell
                    key={modalityColumns[index]}
                    className='py-1 text-end tabular-nums'
                  >
                    {isTokenCount(count) ? (
                      formatNumber(count, locale)
                    ) : (
                      <span
                        className='text-muted-foreground'
                        title={t('Not recorded')}
                        aria-label={t('Not recorded')}
                      >
                        —
                      </span>
                    )}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
