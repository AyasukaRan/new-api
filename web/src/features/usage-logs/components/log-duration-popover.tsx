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

import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { useIsAdmin } from '@/hooks/use-admin'
import { formatUseTime } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { getRequestTrace } from '../api'
import { formatFirstResponseTime } from '../lib/format'

function UpstreamTiming(props: { traceId: string; totalMs: number }) {
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
  if (!isAdmin || user == null) return null
  // Only the final attempted response represents the request that completed.
  const legs = trace.data ?? []
  let lastUpstream
  for (let i = legs.length - 1; i >= 0; i--) {
    if (
      legs[i].direction === 'upstream_request' ||
      legs[i].direction === 'upstream_response'
    ) {
      lastUpstream = legs[i]
      break
    }
  }
  if (lastUpstream?.direction !== 'upstream_response' || !lastUpstream.headers)
    {return null}
  const measurements = []
  for (const [name, values] of Object.entries(lastUpstream.headers)) {
    const key = name.toLowerCase()
    if (
      key !== 'openai-processing-ms' &&
      key !== 'x-envoy-upstream-service-time'
    ) {
      continue
    }
    const value = values?.[0]?.trim()
    if (!value) continue
    const ms = Number(value)
    if (!Number.isFinite(ms) || ms < 0) continue
    measurements.push({
      key,
      label:
        key === 'openai-processing-ms'
          ? t('Upstream processing time')
          : t('Upstream proxy service time'),
      ms,
    })
  }
  if (!measurements.length) return null
  const max = Math.max(1, props.totalMs, ...measurements.map((item) => item.ms))
  return (
    <section className='space-y-1.5 border-t pt-2'>
      <div className='text-muted-foreground text-xs'>
        {t('Reported by upstream')}
      </div>
      {measurements.map((item) => (
        <div key={item.key}>
          <div className='flex items-baseline justify-between gap-4 text-xs'>
            <span>{item.label}</span>
            <span className='font-mono tabular-nums'>
              {formatFirstResponseTime(item.ms)}
            </span>
          </div>
          <div
            aria-hidden='true'
            className='bg-muted mt-1 h-1.5 overflow-hidden rounded-full'
          >
            <div
              className='h-full bg-violet-500 dark:bg-violet-400'
              style={{ width: `${(100 * item.ms) / max}%` }}
            />
          </div>
        </div>
      ))}
    </section>
  )
}

export function LogDurationPopover(props: {
  seconds: number
  firstMs: number | null
  traceId?: string
}) {
  const { t } = useTranslation()
  const ms = props.seconds * 1000
  const first = props.firstMs
  const duration = props.seconds === 0 ? '<1s' : formatUseTime(props.seconds)
  const canSplit = first != null && ms > first
  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant='ghost'
            aria-label={`${t('Timing breakdown')} · ${duration}`}
            onClick={(event) => event.stopPropagation()}
            className='hover:text-primary decoration-muted-foreground/65 h-auto min-h-4 rounded-xs px-0 py-0 font-mono text-xs font-semibold text-inherit underline decoration-dotted underline-offset-4 hover:bg-transparent'
          />
        }
      >
        {duration}
      </PopoverTrigger>
      <PopoverContent
        align='end'
        onClick={(event) => event.stopPropagation()}
        className='w-72 max-w-[calc(100vw-2rem)] gap-2.5 p-3'
      >
        <div className='flex items-center justify-between gap-3'>
          <PopoverTitle>{t('Timing breakdown')}</PopoverTitle>
          <strong className='font-mono tabular-nums'>{duration}</strong>
        </div>
        <div className='text-muted-foreground text-xs'>
          {t('Observed by gateway')}
        </div>
        <div
          aria-hidden='true'
          className='bg-muted flex h-2 overflow-hidden rounded-full'
        >
          {canSplit && (
            <span
              className='bg-sky-500 dark:bg-sky-400'
              style={{ width: `${(100 * first) / ms}%` }}
            />
          )}
          <span
            className={
              canSplit
                ? 'grow bg-emerald-500 dark:bg-emerald-400'
                : 'grow bg-slate-500 dark:bg-slate-400'
            }
          />
        </div>
        <dl className='space-y-1.5 text-xs'>
          {first != null && (
            <div className='flex items-center justify-between'>
              <dt className='flex items-center gap-2'>
                <i
                  aria-hidden
                  className='size-2 rounded-full bg-sky-500 dark:bg-sky-400'
                />
                {t('First token')}
              </dt>
              <dd className='font-mono tabular-nums'>
                {formatFirstResponseTime(first)}
              </dd>
            </div>
          )}
          {canSplit ? (
            <div className='flex items-center justify-between'>
              <dt className='flex items-center gap-2'>
                <i
                  aria-hidden
                  className='size-2 rounded-full bg-emerald-500 dark:bg-emerald-400'
                />
                {t('After first token')}
              </dt>
              <dd className='font-mono tabular-nums'>
                ≈ {formatFirstResponseTime(ms - first)}
              </dd>
            </div>
          ) : (
            <div className='flex items-center justify-between'>
              <dt className='flex items-center gap-2'>
                <i
                  aria-hidden
                  className='size-2 rounded-full bg-slate-500 dark:bg-slate-400'
                />
                {t('Duration')}
              </dt>
              <dd className='font-mono tabular-nums'>{duration}</dd>
            </div>
          )}
        </dl>
        {props.traceId && (
          <UpstreamTiming traceId={props.traceId} totalMs={ms} />
        )}
      </PopoverContent>
    </Popover>
  )
}
