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
import {
  Globe2,
  Info,
  MapPin,
  Minus,
  Pause,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
} from 'lucide-react'
import {
  memo,
  useEffect,
  useEffectEvent,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react'
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
import {
  calculateRequestOriginLayout,
  clampRequestOriginViewport,
  createRequestOriginRoute,
  projectRequestOrigin,
  zoomRequestOriginViewport,
  type RequestOriginViewport,
} from '../../lib/request-origin-layout'
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

const WorldLand = memo(function WorldLand() {
  return <path d={WORLD_LAND_PATH} className='origin-map-land' />
})

function OriginMapCanvas(props: {
  points: RequestOriginPoint[]
  pulses: RequestOriginPulse[]
  selected?: string
  onSelect: (id: string) => void
  reducedMotion: boolean
  hasInternationalActivity: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const countryNames = useMemo(
    () => new Intl.DisplayNames([locale ?? 'en'], { type: 'region' }),
    [locale]
  )
  const gridId = useId()
  const canvas = useRef<SVGSVGElement>(null)
  const [canvasSize, setCanvasSize] = useState({ width: 1000, height: 500 })
  const [manualView, setManualView] = useState<RequestOriginViewport>()
  const [dragging, setDragging] = useState(false)
  const drag = useRef<{
    pointerId: number
    x: number
    y: number
    viewport: RequestOriginViewport
    scale: number
  }>(undefined)
  useLayoutEffect(() => {
    const element = canvas.current
    if (!element) return
    const measure = () => {
      const { width, height } = element.getBoundingClientRect()
      if (!width || !height) return
      setCanvasSize((previous) =>
        previous.width === width && previous.height === height
          ? previous
          : { width, height }
      )
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  const layout = useMemo(
    () =>
      calculateRequestOriginLayout(
        props.hasInternationalActivity ? [] : props.points
      ),
    [props.points, props.hasInternationalActivity]
  )
  const points = props.points.slice(0, 500)
  const [left, top] = layout.viewBox.split(' ').map(Number)
  const aspect = canvasSize.width / canvasSize.height
  const autoWidth = Math.max(layout.width, layout.height * aspect)
  const autoHeight = Math.max(layout.height, layout.width / aspect)
  const viewport = manualView ?? {
    x: left - (autoWidth - layout.width) / 2,
    y: top - (autoHeight - layout.height) / 2,
    width: autoWidth,
    height: autoHeight,
  }
  const scale = Math.max(
    viewport.width / canvasSize.width,
    viewport.height / canvasSize.height
  )
  const handleWheel = useEffectEvent((event: WheelEvent) => {
    const element = canvas.current
    if (!element || event.deltaY === 0) return
    const bounds = element.getBoundingClientRect()
    if (!bounds.width || !bounds.height) return
    event.preventDefault()
    if (drag.current) return
    let delta = event.deltaY
    if (event.deltaMode === 1) delta *= 16
    if (event.deltaMode === 2) delta *= bounds.height
    setManualView((previous) =>
      zoomRequestOriginViewport(
        previous ?? viewport,
        Math.exp(Math.max(-240, Math.min(240, delta)) * 0.002),
        {
          x: Math.max(
            0,
            Math.min(1, (event.clientX - bounds.left) / bounds.width)
          ),
          y: Math.max(
            0,
            Math.min(1, (event.clientY - bounds.top) / bounds.height)
          ),
        }
      )
    )
  })
  useEffect(() => {
    const element = canvas.current
    if (!element) return
    // Wheel zoom must prevent page scrolling while the pointer is over the map.
    element.addEventListener('wheel', handleWheel, { passive: false })
    return () => element.removeEventListener('wheel', handleWheel)
  }, [])
  const endPan = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (drag.current?.pointerId !== event.pointerId) return
    drag.current = undefined
    setDragging(false)
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
  }
  const maxCount = Math.max(1, ...points.map((point) => point.count))
  return (
    <>
      <svg
        ref={canvas}
        viewBox={
          manualView
            ? `${manualView.x} ${manualView.y} ${manualView.width} ${manualView.height}`
            : layout.viewBox
        }
        data-origin-map-mode={layout.mode}
        data-origin-map-view={manualView ? 'manual' : 'auto'}
        className={cn(
          'origin-world-map h-full w-full outline-none',
          dragging && 'origin-map-dragging'
        )}
        role='group'
        tabIndex={0}
        aria-label={
          layout.mode === 'china'
            ? t('Request origin China map')
            : t('Request origin world map')
        }
        onPointerDown={(event) => {
          if (event.button !== 0 || !event.isPrimary) return
          if (
            event.target instanceof Element &&
            event.target.closest('[role="button"]')
          ) {
            return
          }
          drag.current = {
            pointerId: event.pointerId,
            x: event.clientX,
            y: event.clientY,
            viewport,
            scale,
          }
          event.currentTarget.setPointerCapture?.(event.pointerId)
        }}
        onPointerMove={(event) => {
          const start = drag.current
          if (!start || start.pointerId !== event.pointerId) return
          const dx = event.clientX - start.x
          const dy = event.clientY - start.y
          if (Math.hypot(dx, dy) < 3) return
          setDragging(true)
          setManualView(
            clampRequestOriginViewport({
              ...start.viewport,
              x: start.viewport.x - dx * start.scale,
              y: start.viewport.y - dy * start.scale,
            })
          )
        }}
        onPointerUp={endPan}
        onPointerCancel={endPan}
        onLostPointerCapture={endPan}
        onKeyDown={(event) => {
          if (
            event.target !== event.currentTarget ||
            event.altKey ||
            event.ctrlKey ||
            event.metaKey
          ) {
            return
          }
          if (event.key === '+' || event.key === '=' || event.key === '-') {
            event.preventDefault()
            setManualView((previous) =>
              zoomRequestOriginViewport(
                previous ?? viewport,
                event.key === '-' ? 1.25 : 0.8
              )
            )
            return
          }
          if (event.key === 'Home' || event.key === '0') {
            event.preventDefault()
            setManualView(undefined)
            return
          }
          const directions: Record<string, [number, number]> = {
            ArrowLeft: [-1, 0],
            ArrowRight: [1, 0],
            ArrowUp: [0, -1],
            ArrowDown: [0, 1],
          }
          const direction = directions[event.key]
          if (!direction) return
          event.preventDefault()
          setManualView((previous) => {
            const current = previous ?? viewport
            return clampRequestOriginViewport({
              ...current,
              x: current.x + direction[0] * current.width * 0.15,
              y: current.y + direction[1] * current.height * 0.15,
            })
          })
        }}
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
        <g aria-hidden='true' className='origin-map-routes'>
          {points.map((point) => {
            const route = createRequestOriginRoute(point)
            if (!route) return null
            return (
              <path
                key={point.location_id}
                d={route}
                data-origin-route={point.location_id}
                strokeWidth={
                  (0.8 + Math.sqrt(point.count / maxCount) * 2.4) * scale
                }
                className={cn(
                  'origin-map-route',
                  point.location_id === props.selected &&
                    'origin-map-route-selected',
                  props.selected &&
                    point.location_id !== props.selected &&
                    'origin-map-route-dimmed'
                )}
              />
            )
          })}
        </g>
        {points.map((point, index) => {
          const { x, y } = projectRequestOrigin(point)
          const radius = (2.7 + Math.sqrt(point.count / maxCount) * 5) * scale
          const selected = point.location_id === props.selected
          const label = `${locationName(point, countryNames)} · ${t('{{count}} request records', { count: point.count })}`
          const nearGateway =
            Math.hypot(x - layout.gateway.x, y - layout.gateway.y) < 40 * scale
          return (
            <g
              key={point.location_id}
              data-origin-location={point.location_id}
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
              <circle
                r={Math.max(12 * scale, radius + 5 * scale)}
                fill='transparent'
              />
              <circle
                r={radius + 5 * scale}
                className={cn(
                  'origin-map-halo',
                  selected && 'origin-map-halo-selected'
                )}
              />
              <circle r={radius} className='origin-map-dot' />
              <circle r={radius + 9 * scale} className='origin-map-focus' />
              {(selected ||
                (!props.selected &&
                  canvasSize.width >= 600 &&
                  index === 0)) && (
                <text
                  y={(nearGateway ? -22 : 25) * scale}
                  textAnchor='middle'
                  fontSize={12 * scale}
                  className='origin-map-location-label'
                  aria-hidden='true'
                >
                  {point.city ||
                    point.region ||
                    countryName(point, countryNames)}
                  <tspan className='origin-map-count'>
                    {' · '}
                    {formatCompactNumber(point.count, locale)}
                  </tspan>
                </text>
              )}
            </g>
          )
        })}
        {points.length > 0 && (
          <g
            transform={`translate(${layout.gateway.x} ${layout.gateway.y})`}
            className='origin-map-gateway'
            aria-label={t('Shiyan, Hubei · Gateway')}
          >
            <circle r={15 * scale} className='origin-map-gateway-ring' />
            <circle r={9 * scale} className='origin-map-gateway-ring' />
            <circle r={5 * scale} className='origin-map-dot' />
            <text
              y={46 * scale}
              textAnchor='middle'
              fontSize={13 * scale}
              className='origin-map-gateway-label'
            >
              {t('Shiyan, Hubei · Gateway')}
            </text>
          </g>
        )}
        {!props.reducedMotion &&
          props.pulses.map((point) => {
            const { x, y } = projectRequestOrigin(point)
            const route = createRequestOriginRoute(point)
            return (
              <g
                key={point.pulse_id}
                aria-hidden='true'
                data-origin-pulse={point.pulse_id}
                className='origin-map-activity'
              >
                <g transform={`translate(${x} ${y}) scale(${scale})`}>
                  <circle r='4' className='origin-map-pulse' />
                </g>
                {route && (
                  <>
                    <path
                      d={route}
                      pathLength='1'
                      strokeWidth={2.2 * scale}
                      className='origin-map-flight-trail'
                    />
                    <circle
                      r={5 * scale}
                      style={{ offsetPath: `path("${route}")` }}
                      className='origin-map-flight-head origin-map-flight-glow'
                    />
                    <circle
                      r={2.6 * scale}
                      style={{ offsetPath: `path("${route}")` }}
                      className='origin-map-flight-head'
                    />
                    <g
                      transform={`translate(${layout.gateway.x} ${layout.gateway.y}) scale(${scale})`}
                    >
                      <circle r='5' className='origin-map-arrival' />
                    </g>
                  </>
                )}
              </g>
            )
          })}
      </svg>
      <div
        className='bg-card/95 absolute right-2 bottom-2 flex items-center gap-0.5 rounded-lg border p-0.5 shadow-sm'
        role='group'
        aria-label={t('Zoom')}
      >
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                aria-label={t('Zoom in')}
                disabled={viewport.width <= 40}
                onClick={() =>
                  setManualView((previous) =>
                    zoomRequestOriginViewport(previous ?? viewport, 0.8)
                  )
                }
              />
            }
          >
            <Plus className='size-4' />
          </TooltipTrigger>
          <TooltipContent>{t('Zoom in')}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                aria-label={t('Zoom out')}
                disabled={viewport.width >= 1000 && viewport.height >= 500}
                onClick={() =>
                  setManualView((previous) =>
                    zoomRequestOriginViewport(previous ?? viewport, 1.25)
                  )
                }
              />
            }
          >
            <Minus className='size-4' />
          </TooltipTrigger>
          <TooltipContent>{t('Zoom out')}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                aria-label={t('Reset view')}
                disabled={!manualView}
                onClick={() => setManualView(undefined)}
              />
            }
          >
            <RotateCcw className='size-3.5' />
          </TooltipTrigger>
          <TooltipContent>{t('Reset view')}</TooltipContent>
        </Tooltip>
      </div>
    </>
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
  const { query, status, pulses, live, isAdmin, hasInternationalActivity } =
    useRequestOrigins({
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
      loading={query.isPending}
      height='h-72'
      contentClassName='p-0 sm:p-0'
      headerActions={
        <div className='flex items-center gap-2'>
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant='ghost'
                  size='icon'
                  className='size-7'
                  aria-label={t('Details')}
                />
              }
            >
              <Info className='size-3.5' />
            </TooltipTrigger>
            <TooltipContent className='max-w-xs space-y-2'>
              <p>
                {props.params
                  ? t('Locations in the selected range')
                  : t('Request origins in the last {{hours}} hours', {
                      hours: props.hours ?? 24,
                    })}
              </p>
              <p>{t('Marker size and route width show request counts.')}</p>
              <p>
                {t(
                  'Locations are approximate and may reflect a proxy or gateway.'
                )}{' '}
                {t('Routes show request direction only.')}
              </p>
              {data?.truncated && (
                <p>
                  {t(
                    'Showing the latest {{count}} request records. Locations are approximate.',
                    { count: data.total }
                  )}
                </p>
              )}
              {points.length > 500 && (
                <p>
                  {t('Map shows the top {{count}} locations.', { count: 500 })}
                </p>
              )}
            </TooltipContent>
          </Tooltip>
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
                  points={points}
                  pulses={pulses}
                  selected={selected?.location_id}
                  onSelect={setSelectedId}
                  reducedMotion={reducedMotion}
                  hasInternationalActivity={hasInternationalActivity}
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
                      />
                    </div>
                  </div>
                )}
              </div>
              {selected && (
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
          <div className='text-muted-foreground flex justify-end px-4 py-2 text-[10px] sm:px-5'>
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
