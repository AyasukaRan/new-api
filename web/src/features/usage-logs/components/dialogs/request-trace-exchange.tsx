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
import { AlertTriangle } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useMediaQuery } from '@/hooks/use-media-query'

import type { RequestTraceDirection, RequestTraceLeg } from '../../types'
import {
  RequestTraceRawLeg,
  RequestTraceRenderedLeg,
} from './request-trace-leg'

const DIRECTION_LABELS: Record<RequestTraceDirection, string> = {
  client_request: 'Client → new-api',
  upstream_request: 'new-api → Upstream',
  upstream_response: 'Upstream → new-api',
  client_response: 'new-api → Client',
}

export function RequestTraceExchange(props: {
  traceId: string
  legs: RequestTraceLeg[]
}) {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 767px)')
  const selectedTabRef = useRef<HTMLButtonElement>(null)
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null)
  const legs = useMemo(
    () => [...props.legs].sort((a, b) => a.seq - b.seq),
    [props.legs]
  )
  // The client legs belong to the whole exchange; only upstream legs have
  // retry indices. Keep all of them reachable when inspecting a retry.
  const latestFirst = [...legs].reverse()
  const defaultLeg =
    latestFirst.find((leg) => leg.direction === 'client_response') ??
    latestFirst.find((leg) => leg.direction === 'upstream_response') ??
    legs[0]
  const selectedLeg = legs.find((leg) => leg.seq === selectedSeq) ?? defaultLeg
  useEffect(() => {
    selectedTabRef.current?.scrollIntoView({
      block: 'nearest',
      inline: 'nearest',
    })
  }, [isMobile, selectedLeg?.seq])
  if (!selectedLeg) return null

  return (
    <Tabs
      orientation={isMobile ? 'horizontal' : 'vertical'}
      value={selectedLeg.seq}
      onValueChange={(value) => setSelectedSeq(Number(value))}
      className='h-full min-h-0 min-w-0 gap-3 max-md:flex-col'
    >
      <div className='shrink-0 overflow-auto overscroll-contain md:w-64 md:border-r md:pr-3'>
        <TabsList
          aria-label={t('Request exchanges')}
          aria-orientation={isMobile ? 'horizontal' : 'vertical'}
          className='h-auto w-max min-w-full items-stretch justify-start gap-1 bg-transparent p-0 data-horizontal:h-auto md:w-full md:min-w-0'
        >
          {legs.map((leg) => {
            const isUpstream = leg.direction.startsWith('upstream_')
            const attemptLabel = t('Attempt {{index}}', {
              index: leg.attempt + 1,
            })
            const label = t(DIRECTION_LABELS[leg.direction])
            return (
              <TabsTrigger
                key={leg.seq}
                ref={leg.seq === selectedLeg.seq ? selectedTabRef : undefined}
                value={leg.seq}
                aria-label={isUpstream ? `${label} · ${attemptLabel}` : label}
                className='data-active:bg-muted h-auto flex-none flex-col items-start gap-1.5 rounded-lg px-3 py-2.5 text-left data-active:shadow-none md:whitespace-normal'
              >
                <span className='text-sm font-medium'>{label}</span>
                <span className='text-muted-foreground flex flex-wrap items-center gap-1.5 text-xs'>
                  {isUpstream && <span>{attemptLabel}</span>}
                  {leg.channel_id > 0 && (
                    <span>
                      {t('Channel')} #{leg.channel_id}
                    </span>
                  )}
                  {leg.status > 0 && (
                    <StatusBadge
                      label={String(leg.status)}
                      variant={leg.status >= 400 ? 'danger' : 'success'}
                      copyable={false}
                      size='sm'
                    />
                  )}
                </span>
              </TabsTrigger>
            )
          })}
        </TabsList>
      </div>
      {legs.map((leg) => (
        <TabsContent
          key={leg.seq}
          value={leg.seq}
          className='min-h-0 min-w-0 overflow-y-auto overscroll-contain md:pl-1'
        >
          <RequestTraceExchangeDetail traceId={props.traceId} leg={leg} />
        </TabsContent>
      ))}
    </Tabs>
  )
}

function RequestTraceExchangeDetail(props: {
  traceId: string
  leg: RequestTraceLeg
}) {
  const { t } = useTranslation()
  const leg = props.leg
  return (
    <div className='min-w-0 space-y-4 px-1 pb-2'>
      <div className='space-y-2'>
        <h3 className='text-sm font-semibold'>
          {t(DIRECTION_LABELS[leg.direction])}
        </h3>
        <div className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs'>
          {leg.status > 0 && (
            <StatusBadge
              label={`HTTP ${leg.status}`}
              variant={leg.status >= 400 ? 'danger' : 'success'}
              copyable={false}
            />
          )}
          {leg.channel_id > 0 && (
            <span>
              {t('Channel')} #{leg.channel_id}
            </span>
          )}
          {leg.format && <span className='font-mono'>{leg.format}</span>}
          {leg.body_size > 0 && (
            <span>
              {t('Payload: {{bytes}} bytes', {
                bytes: leg.body_size.toLocaleString(),
              })}
            </span>
          )}
        </div>
        {leg.url && (
          <p className='text-muted-foreground font-mono text-xs break-all'>
            {leg.method} {leg.url}
          </p>
        )}
      </div>
      {leg.truncated && (
        <Alert>
          <AlertTriangle className='size-4' aria-hidden='true' />
          <AlertDescription>
            {t(
              'Only the beginning and the end were kept. The original payload was {{bytes}} bytes.',
              { bytes: leg.body_size }
            )}
          </AlertDescription>
        </Alert>
      )}
      <Tabs defaultValue={leg.rendered ? 'rendered' : 'body'}>
        <TabsList variant='line' aria-label={t('Payload views')}>
          {leg.rendered && (
            <TabsTrigger value='rendered'>{t('Rendered')}</TabsTrigger>
          )}
          <TabsTrigger value='body'>{t('Body')}</TabsTrigger>
          <TabsTrigger value='headers'>{t('Headers')}</TabsTrigger>
        </TabsList>
        {leg.rendered && (
          <TabsContent value='rendered' className='min-w-0 pt-2'>
            <RequestTraceRenderedLeg
              leg={leg}
              label={DIRECTION_LABELS[leg.direction]}
              showAttempt={leg.direction.startsWith('upstream_')}
            />
          </TabsContent>
        )}
        <TabsContent value='body' className='min-w-0 pt-2'>
          <RequestTraceRawLeg traceId={props.traceId} leg={leg} view='body' />
        </TabsContent>
        <TabsContent value='headers' className='min-w-0 pt-2'>
          <RequestTraceRawLeg
            traceId={props.traceId}
            leg={leg}
            view='headers'
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}
