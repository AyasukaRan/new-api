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
import { useTranslation } from 'react-i18next'

import { useIsAdmin } from '@/hooks/use-admin'
import { toIntlLocale } from '@/i18n/languages'
import { useAuthStore } from '@/stores/auth-store'

import { getRequestTrace } from '../api'
import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'

type TimingBand = { key: string; label: string; ms: number; color: string }

const waitColor = 'bg-amber-500 dark:bg-amber-400'
const outputColor = 'bg-emerald-500 dark:bg-emerald-400'
const timingNumber = 'font-mono text-xs font-medium tabular-nums'
const sectionTitle = 'text-sm font-medium'

function SegmentedBar({ bands }: { bands: TimingBand[] }) {
  return (
    <div
      aria-hidden='true'
      className='bg-muted flex h-2.5 overflow-hidden rounded-full'
    >
      {bands.map((band) => (
        <span
          key={band.key}
          className={band.color}
          style={{ flexGrow: band.ms, flexBasis: 0 }}
        />
      ))}
    </div>
  )
}

function UpstreamTiming(props: {
  traceId: string
  formatMs: (ms: number) => string
  totalMs: number
}) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const trace = useQuery({
    queryKey: [
      'usage-logs',
      'request-trace',
      user?.id,
      user?.role,
      props.traceId,
    ],
    queryFn: ({ signal }) => getRequestTrace(props.traceId, signal),
    enabled: isAdmin && user != null,
    retry: false,
    staleTime: 60_000,
    gcTime: 0,
    placeholderData: undefined,
  })
  if (!isAdmin || !user) return null
  const legs = trace.data ?? []
  let final
  for (let index = legs.length - 1; index >= 0; index--) {
    if (
      legs[index].direction === 'upstream_request' ||
      legs[index].direction === 'upstream_response'
    ) {
      final = legs[index]
      break
    }
  }
  if (final?.direction !== 'upstream_response' || !final.headers) return null
  const durations = new Map<number, string[]>()
  for (const [name, values] of Object.entries(final.headers)) {
    const key = name.toLowerCase()
    if (
      key !== 'openai-processing-ms' &&
      key !== 'x-envoy-upstream-service-time'
    ) {
      continue
    }
    const raw = values?.[0]?.trim()
    if (!raw || !/^\d+(?:\.\d+)?$/.test(raw)) continue
    const ms = Number(raw)
    if (!Number.isFinite(ms)) continue
    const label =
      key === 'openai-processing-ms'
        ? t('Upstream processing time')
        : t('Upstream proxy service time')
    const names = durations.get(ms) ?? []
    if (!names.includes(label)) names.push(label)
    durations.set(ms, names)
  }
  if (!durations.size) return null
  const scale = Math.max(1, props.totalMs, ...durations.keys())
  return (
    <section className='space-y-3 border-t pt-3'>
      <h3 className={sectionTitle}>{t('Reported by upstream')}</h3>
      <dl className='space-y-2'>
        {Array.from(durations, ([ms, names]) => (
          <div
            key={ms}
            className='grid grid-cols-[minmax(5rem,1fr)_minmax(2rem,1fr)_auto] items-center gap-3 text-xs'
          >
            <dt className='text-muted-foreground'>{names.join(' / ')}</dt>
            <dd className='contents'>
              <span
                aria-hidden='true'
                className='bg-muted h-1.5 overflow-hidden rounded-full'
              >
                <span
                  className='block h-full rounded-full bg-violet-500 dark:bg-violet-400'
                  style={{ width: `${(100 * ms) / scale}%` }}
                />
              </span>
              <span className={timingNumber}>{props.formatMs(ms)}</span>
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

export function LogTimingDetails(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
}) {
  const { t, i18n } = useTranslation()
  const { log, other } = props
  const number = new Intl.NumberFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    { maximumFractionDigits: 3 }
  )
  const formatMs = (ms: number) =>
    ms < 1000 ? `${number.format(ms)}ms` : `${number.format(ms / 1000)}s`
  const rawTotal = other?.duration_ms
  const exact = rawTotal != null && Number.isFinite(rawTotal) && rawTotal >= 0
  const total = exact ? rawTotal : log.use_time * 1000
  const first =
    log.is_stream &&
    other?.frt != null &&
    Number.isFinite(other.frt) &&
    (other.frt > 0 || (exact && other.frt === 0))
      ? other.frt
      : null
  // Legacy usage records subtract two integer Unix timestamps. Neither endpoint's
  // fractional second survives; the true elapsed time can differ by up to a second.
  const upper = exact ? total : total + 1000
  const lower = exact ? total : Math.max(0, total - 1000)
  const canSplit =
    total >= 0 && first != null && (exact ? first <= upper : first < upper)
  const hasTotal = Number.isFinite(total) && total >= 0
  let totalLabel = t('Not recorded')
  if (hasTotal) {
    totalLabel = formatMs(total)
    if (!exact) totalLabel = total === 0 ? '<1s' : `≈ ${totalLabel}`
  }
  const bands: TimingBand[] = []
  let afterLabel: string | undefined
  if (first != null && canSplit) {
    bands.push({
      key: 'first',
      label: t('Time to first token'),
      ms: first,
      color: waitColor,
    })
    const minAfter = Math.max(0, lower - first)
    const maxAfter = upper - first
    bands.push({
      key: 'output',
      label: t('After first token'),
      ms: minAfter,
      color: outputColor,
    })
    if (exact) {
      afterLabel = formatMs(maxAfter)
    } else {
      afterLabel =
        minAfter > 0
          ? `${formatMs(minAfter)} – <${formatMs(maxAfter)}`
          : `<${formatMs(maxAfter)}`
      bands.push({
        key: 'uncertainty',
        label: '',
        ms: maxAfter - minAfter,
        color:
          'opacity-50 bg-[repeating-linear-gradient(135deg,var(--muted-foreground)_0px,var(--muted-foreground)_3px,transparent_3px,transparent_6px)]',
      })
    }
  }

  const events =
    props.isAdmin && Array.isArray(other?.admin_info?.request_policy)
      ? other.admin_info.request_policy
      : []
  const routing: TimingBand[] = []
  let attempt: { number: number; start: number } | undefined
  let endedAt: number | undefined
  let last = 0
  for (const event of events) {
    if (
      !event ||
      !Number.isInteger(event.attempt) ||
      !Number.isFinite(event.elapsed_ms) ||
      event.elapsed_ms < last
    ) {
      routing.length = 0
      break
    }
    last = event.elapsed_ms
    if (event.decision?.action === 'attempt') {
      if (attempt || (endedAt == null && event.attempt !== 1)) {
        routing.length = 0
        break
      }
      if (last > (endedAt ?? 0)) {
        routing.push({
          key: `wait-${event.attempt}-${last}`,
          label:
            endedAt == null ? t('Channel selection') : t('Between attempts'),
          ms: last - (endedAt ?? 0),
          color: 'bg-sky-500 dark:bg-sky-400',
        })
      }
      attempt = { number: event.attempt, start: last }
    } else if (
      attempt &&
      event.attempt === attempt.number &&
      ['success', 'failure', 'stop'].includes(event.decision?.action)
    ) {
      routing.push({
        key: `attempt-${attempt.number}-${attempt.start}`,
        label: t('Attempt {{index}}', { index: attempt.number }),
        ms: last - attempt.start,
        color: event.decision.action === 'success' ? outputColor : waitColor,
      })
      endedAt = last
      attempt = undefined
    }
  }
  const traceId = props.isAdmin ? other?.admin_info?.trace_id : undefined
  return (
    <div className='space-y-4'>
      <section className='space-y-2.5'>
        <div className='flex items-baseline justify-between gap-3'>
          <h3 className={sectionTitle}>{t('Observed by gateway')}</h3>
          <strong className='font-mono text-lg font-semibold tabular-nums'>
            {totalLabel}
          </strong>
        </div>
        {bands.length > 0 && <SegmentedBar bands={bands} />}
        {first == null && hasTotal && total > 0 && (
          <div
            aria-hidden='true'
            className='bg-muted-foreground/35 h-2.5 rounded-full'
          />
        )}
        {first != null && (
          <dl className='grid grid-cols-1 gap-x-5 gap-y-1.5 text-xs sm:grid-cols-2'>
            <div className='flex items-center justify-between gap-1'>
              <dt className='text-muted-foreground flex items-center gap-1.5'>
                <i
                  aria-hidden
                  className={`size-2 shrink-0 rounded-sm ${waitColor}`}
                />
                {t('Time to first token')}
              </dt>
              <dd className={timingNumber}>{formatMs(first)}</dd>
            </div>
            {afterLabel && (
              <div className='flex items-center justify-between gap-1'>
                <dt className='text-muted-foreground flex items-center gap-1.5'>
                  <i
                    aria-hidden
                    className={`size-2 shrink-0 rounded-sm ${outputColor}`}
                  />
                  {t('After first token')}
                </dt>
                <dd className={timingNumber}>{afterLabel}</dd>
              </div>
            )}
          </dl>
        )}
        {hasTotal && !exact && (
          <p className='text-muted-foreground text-xs'>
            {canSplit
              ? t(
                  'The striped range reflects the one-second precision of this log.'
                )
              : t('Total duration was recorded to whole seconds.')}
          </p>
        )}
      </section>
      {routing.length > 0 && (
        <section className='space-y-2.5 border-t pt-3'>
          <div className='flex items-baseline justify-between gap-3'>
            <h3 className={sectionTitle}>{t('Routing observations')}</h3>
            <span className='text-muted-foreground text-xs'>
              {t('Independent timing')}
            </span>
          </div>
          <SegmentedBar bands={routing} />
          <dl className='grid gap-x-5 gap-y-1.5 text-xs sm:grid-cols-2'>
            {routing.map((band) => (
              <div
                key={band.key}
                className='flex items-center justify-between gap-2'
              >
                <dt className='text-muted-foreground flex items-center gap-1.5'>
                  <i
                    aria-hidden
                    className={`size-2 shrink-0 rounded-sm ${band.color}`}
                  />
                  {band.label}
                </dt>
                <dd className={timingNumber}>{formatMs(band.ms)}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}
      {traceId && (
        <UpstreamTiming traceId={traceId} formatMs={formatMs} totalMs={total} />
      )}
    </div>
  )
}
