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
import { Globe2, MapPin, Pause, Play, Radio, RefreshCw } from 'lucide-react'
import { memo, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useMediaQuery } from '@/hooks/use-media-query'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  useRequestOrigins,
  type RequestOriginPulse,
} from '../../hooks/use-request-origins'
import type {
  RequestOriginPoint,
  RequestOriginsParams,
} from '../../lib/request-origins-api'
import { WORLD_LAND_PATH } from '../../lib/world-land-path'
import { PanelWrapper } from '../ui/panel-wrapper'

import '@/styles/request-origin-map.css'

function countryName(
  point: RequestOriginPoint,
  names: Intl.DisplayNames
): string {
  if (!/^[A-Z]{2}$/.test(point.country_code)) return point.country
  return names.of(point.country_code) || point.country
}

function locationName(
  point: RequestOriginPoint,
  names: Intl.DisplayNames
): string {
  return [
    ...new Set(
      [point.city, point.region, countryName(point, names)].filter(Boolean)
    ),
  ].join(', ')
}

function mapPosition(point: RequestOriginPoint) {
  return {
    x: ((point.longitude + 180) * 1000) / 360,
    y: ((90 - point.latitude) * 500) / 180,
  }
}

const WorldLand = memo(function WorldLand() {
  return <path d={WORLD_LAND_PATH} className='origin-map-land' />
})

function OriginMapCanvas(props: {
  points: RequestOriginPoint[]
  pulses: RequestOriginPulse[]
  selected?: string
  onSelect: (id: string) => void
  reducedMotion: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const countryNames = useMemo(
    () => new Intl.DisplayNames([locale ?? 'en'], { type: 'region' }),
    [locale]
  )
  const gridId = useId()
  const maxCount = Math.max(1, ...props.points.map((point) => point.count))
  return (
    <svg
      viewBox='0 0 1000 500'
      className='origin-world-map h-full w-full'
      role='group'
      aria-label={t('Request origin world map')}
    >
      <defs>
        <pattern
          id={gridId}
          width='83.333'
          height='83.333'
          patternUnits='userSpaceOnUse'
        >
          <path
            d='M83.333 0H0V83.333'
            fill='none'
            className='origin-map-grid'
          />
        </pattern>
      </defs>
      <rect
        width='1000'
        height='500'
        fill={`url(#${gridId})`}
        aria-hidden='true'
      />
      <g aria-hidden='true'>
        <WorldLand />
      </g>
      {props.points.map((point) => {
        const { x, y } = mapPosition(point)
        const radius = 2.7 + Math.sqrt(point.count / maxCount) * 5
        const selected = point.location_id === props.selected
        const label = `${locationName(point, countryNames)} · ${t('{{count}} request records', { count: point.count })}`
        return (
          <g
            key={point.location_id}
            transform={`translate(${x} ${y})`}
            role='button'
            tabIndex={0}
            aria-label={label}
            aria-pressed={selected}
            className='origin-map-marker outline-none'
            onClick={() => props.onSelect(point.location_id)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                props.onSelect(point.location_id)
              }
            }}
          >
            <title>
              {locationName(point, countryNames)} ·{' '}
              {formatNumber(point.count, locale)}
            </title>
            <circle r={Math.max(12, radius + 5)} fill='transparent' />
            <circle
              r={radius + 5}
              className={cn(
                'origin-map-halo',
                selected && 'origin-map-halo-selected'
              )}
            />
            <circle r={radius} className='origin-map-dot' />
            <circle r={radius + 9} className='origin-map-focus' />
          </g>
        )
      })}
      {!props.reducedMotion &&
        props.pulses.map((point) => {
          const { x, y } = mapPosition(point)
          return (
            <g
              key={point.pulse_id}
              transform={`translate(${x} ${y})`}
              aria-hidden='true'
              data-origin-pulse={point.pulse_id}
            >
              <circle r='4' className='origin-map-pulse' />
              <circle
                r='3'
                className='origin-map-pulse origin-map-pulse-delayed'
              />
              <circle r='3.5' className='origin-map-flash' />
            </g>
          )
        })}
    </svg>
  )
}

export function RequestOriginMap(props: {
  params?: RequestOriginsParams
  hours?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const countryNames = useMemo(
    () => new Intl.DisplayNames([locale ?? 'en'], { type: 'region' }),
    [locale]
  )
  const [paused, setPaused] = useState(false)
  const [selectedId, setSelectedId] = useState<string>()
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const { query, status, pulses, live, isAdmin } = useRequestOrigins({
    params: props.params,
    hours: props.hours ?? 24,
    paused,
  })
  const data = query.data
  const points = useMemo(
    () => [...(data?.points ?? [])].sort((a, b) => b.count - a.count),
    [data?.points]
  )
  const countries = useMemo(
    () =>
      new Set(points.map((point) => point.country_code).filter(Boolean)).size,
    [points]
  )
  const selected = points.find((point) => point.location_id === selectedId)
  let statusLabel = t('Connecting')
  if (status === 'live') statusLabel = t('Live')
  if (status === 'reconnecting') statusLabel = t('Reconnecting')
  if (status === 'paused') statusLabel = t('Paused')
  if (status === 'history') statusLabel = t('Historical range')

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <Globe2 className='text-muted-foreground size-4' aria-hidden='true' />
          {t('Request origins')}
        </span>
      }
      description={
        props.params
          ? t('Locations in the selected range')
          : t('Request origins in the last {{hours}} hours', {
              hours: props.hours ?? 24,
            })
      }
      loading={query.isPending}
      height='h-72'
      contentClassName='p-0 sm:p-0'
      headerActions={
        <div className='flex items-center gap-2'>
          <span className='text-muted-foreground mr-1 max-w-40 truncate text-xs'>
            {isAdmin
              ? props.params?.username || t('All users')
              : t('Only mine')}
          </span>
          {data?.geoip_available && (
            <span
              className='text-muted-foreground inline-flex items-center gap-1.5 text-xs'
              role='status'
            >
              <span
                className={cn(
                  'size-1.5 rounded-full bg-muted-foreground/50',
                  status === 'live' && 'bg-emerald-500'
                )}
              />
              {statusLabel}
            </span>
          )}
          {live && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant='ghost'
                    size='icon'
                    className='size-7'
                    aria-label={
                      paused ? t('Resume live map') : t('Pause live map')
                    }
                    onClick={() => setPaused(!paused)}
                  />
                }
              >
                {paused ? (
                  <Play className='size-3.5' />
                ) : (
                  <Pause className='size-3.5' />
                )}
              </TooltipTrigger>
              <TooltipContent>
                {paused ? t('Resume live map') : t('Pause live map')}
              </TooltipContent>
            </Tooltip>
          )}
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant='ghost'
                  size='icon'
                  className='size-7'
                  aria-label={t('Refresh')}
                  disabled={query.isFetching}
                  onClick={() => void query.refetch()}
                />
              }
            >
              <RefreshCw
                className={cn(
                  'size-3.5',
                  query.isFetching && 'motion-safe:animate-spin'
                )}
              />
            </TooltipTrigger>
            <TooltipContent>{t('Refresh')}</TooltipContent>
          </Tooltip>
        </div>
      }
    >
      {query.isError && !data ? (
        <ErrorState
          className='min-h-72'
          title={t('Failed to load request origins')}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <>
          <div className='grid min-w-0 grid-cols-1 xl:grid-cols-[minmax(0,7fr)_minmax(17rem,3fr)]'>
            <div className='relative min-w-0 px-3 pt-3 pb-2 sm:px-5'>
              <div className='relative z-10 mb-2 flex flex-wrap gap-x-6 gap-y-2 text-xs'>
                <span className='text-muted-foreground'>
                  {t('Request records')}{' '}
                  <strong className='text-foreground ml-1.5 tabular-nums'>
                    {formatNumber(data?.total ?? 0, locale)}
                  </strong>
                </span>
                <span className='text-muted-foreground'>
                  {t('Located')}{' '}
                  <strong className='text-foreground ml-1.5 tabular-nums'>
                    {formatNumber(data?.located ?? 0, locale)}
                  </strong>
                </span>
                <span className='text-muted-foreground'>
                  {t('Countries / regions')}{' '}
                  <strong className='text-foreground ml-1.5 tabular-nums'>
                    {formatNumber(countries, locale)}
                  </strong>
                </span>
              </div>
              <div className='relative mx-auto aspect-[2/1] max-h-[680px] w-full'>
                <OriginMapCanvas
                  points={points.slice(0, 500)}
                  pulses={pulses}
                  selected={selected?.location_id}
                  onSelect={setSelectedId}
                  reducedMotion={reducedMotion}
                />
                {points.length === 0 && (
                  <div className='absolute inset-0 flex items-center justify-center'>
                    <div className='bg-card/90 mx-3 rounded-xl backdrop-blur-sm'>
                      <EmptyState
                        icon={MapPin}
                        className='min-h-0 px-4 py-4'
                        title={
                          data?.geoip_available
                            ? t('No located requests')
                            : t('GeoIP database unavailable')
                        }
                        description={
                          data?.geoip_available
                            ? t(
                                'Private or unrecognized addresses are not plotted.'
                              )
                            : t(
                                'Request locations will appear when the GeoIP database is available.'
                              )
                        }
                      />
                    </div>
                  </div>
                )}
              </div>
              {selected ? (
                <div className='flex min-h-8 items-center gap-2 px-1 text-xs'>
                  <MapPin
                    className='text-primary size-3.5 shrink-0'
                    aria-hidden='true'
                  />
                  <span className='min-w-0 truncate'>
                    {locationName(selected, countryNames)}
                  </span>
                  <strong className='ml-auto shrink-0 tabular-nums'>
                    {t('{{count}} request records', { count: selected.count })}
                  </strong>
                </div>
              ) : (
                <div className='text-muted-foreground flex min-h-8 items-center gap-1.5 px-1 text-xs'>
                  <Radio className='size-3.5 shrink-0' aria-hidden='true' />
                  {t('New request records light up their source location.')}
                </div>
              )}
            </div>
            <div className='border-t px-4 py-4 sm:px-5 xl:border-t-0 xl:border-l'>
              <h4 className='mb-3 text-xs font-semibold'>
                {t('Top locations')}
              </h4>
              {points.length > 0 ? (
                <ol className='grid grid-cols-1 gap-1 sm:grid-cols-2 sm:gap-x-4 xl:grid-cols-1'>
                  {points.slice(0, 7).map((point, index) => (
                    <li key={point.location_id}>
                      <Button
                        variant='ghost'
                        className={cn(
                          'h-auto w-full justify-start gap-2 rounded-lg px-2 py-2 text-left',
                          selected?.location_id === point.location_id &&
                            'bg-accent'
                        )}
                        onClick={() => setSelectedId(point.location_id)}
                        aria-pressed={
                          selected?.location_id === point.location_id
                        }
                      >
                        <span className='text-muted-foreground w-3 shrink-0 text-[10px] tabular-nums'>
                          {index + 1}
                        </span>
                        <span className='min-w-0 flex-1'>
                          <span className='flex items-center gap-2'>
                            <span className='min-w-0 flex-1 truncate text-xs font-medium'>
                              {point.city ||
                                point.region ||
                                countryName(point, countryNames)}
                            </span>
                            <span className='shrink-0 text-xs tabular-nums'>
                              {formatCompactNumber(point.count, locale)}
                            </span>
                          </span>
                          <span className='text-muted-foreground mt-0.5 block truncate text-[11px] font-normal'>
                            {countryName(point, countryNames)}
                          </span>
                          <Progress
                            value={
                              (point.count / Math.max(1, data?.located ?? 0)) *
                              100
                            }
                            aria-label={locationName(point, countryNames)}
                            className='mt-1.5 opacity-65'
                          />
                        </span>
                      </Button>
                    </li>
                  ))}
                </ol>
              ) : (
                <p className='text-muted-foreground py-3 text-xs'>
                  {t('No data available')}
                </p>
              )}
              {!!data && data.unmapped > 0 && (
                <div className='text-muted-foreground mt-3 space-y-1.5 border-t pt-3 text-xs'>
                  <div className='flex justify-between gap-2'>
                    <span>{t('Unlocated')}</span>
                    <span className='tabular-nums'>
                      {formatNumber(data.unmapped, locale)}
                    </span>
                  </div>
                  {data.private > 0 && (
                    <div className='flex justify-between gap-2'>
                      <span>{t('Private addresses')}</span>
                      <span className='tabular-nums'>
                        {formatNumber(data.private, locale)}
                      </span>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
          {query.isError && data && (
            <p className='px-4 pb-2 text-xs text-amber-600 dark:text-amber-400'>
              {t(
                'Live counts could not be refreshed. Showing the last snapshot.'
              )}
            </p>
          )}
          <div className='text-muted-foreground flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t px-4 py-2.5 text-[11px] sm:px-5'>
            <span>
              {data?.truncated
                ? t(
                    'Showing the latest {{count}} request records. Locations are approximate.',
                    { count: data.total }
                  )
                : t(
                    'Locations are approximate and may reflect a proxy or gateway.'
                  )}{' '}
              {points.length > 500 &&
                t('Map shows the top {{count}} locations.', { count: 500 })}
            </span>
            <a
              href='https://db-ip.com'
              target='_blank'
              rel='noreferrer'
              className='shrink-0 underline-offset-2 hover:underline'
            >
              IP Geolocation by DB-IP
            </a>
          </div>
        </>
      )}
    </PanelWrapper>
  )
}
