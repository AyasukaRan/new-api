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
import type { RequestOriginPoint } from './request-origins-api'

type OriginCoordinates = Pick<RequestOriginPoint, 'latitude' | 'longitude'>

export interface RequestOriginPosition {
  x: number
  y: number
}

export interface RequestOriginViewport extends RequestOriginPosition {
  width: number
  height: number
}

export interface RequestOriginLayout {
  viewBox: string
  mode: 'china' | 'world'
  gateway: RequestOriginPosition
  width: number
  height: number
  scale: number
}

// User-provided gateway city; Natural Earth populated-place coordinates.
export const REQUEST_ORIGIN_GATEWAY: OriginCoordinates = {
  longitude: 110.779997,
  latitude: 32.570039,
}

export function projectRequestOrigin(
  point: OriginCoordinates
): RequestOriginPosition {
  const longitude = ((((point.longitude - 110 + 180) % 360) + 360) % 360) - 180
  return {
    x: ((longitude + 180) * 1000) / 360,
    y: ((90 - point.latitude) * 500) / 180,
  }
}

export function isDomesticRequestOrigin(
  point: Pick<RequestOriginPoint, 'country_code'>
): boolean {
  return ['CN', 'HK', 'MO', 'TW'].includes(point.country_code)
}

export function calculateRequestOriginLayout(
  points: readonly Pick<RequestOriginPoint, 'country_code'>[]
): RequestOriginLayout {
  const domestic = points.length > 0 && points.every(isDomesticRequestOrigin)
  const width = domestic ? 220 : 1000
  const height = domestic ? 155 : 500
  return {
    // China bounds include route curvature and marker halos around the edges.
    viewBox: domestic ? '374.44 70 220 155' : '0 0 1000 500',
    mode: domestic ? 'china' : 'world',
    gateway: projectRequestOrigin(REQUEST_ORIGIN_GATEWAY),
    width,
    height,
    // Preserve marker sizes in the map's existing 2:1 SVG viewport.
    scale: Math.max(width / 1000, height / 500),
  }
}

export function createRequestOriginRoute(
  point: OriginCoordinates
): string | null {
  const origin = projectRequestOrigin(point)
  const gateway = projectRequestOrigin(REQUEST_ORIGIN_GATEWAY)
  const distance = Math.hypot(origin.x - gateway.x, origin.y - gateway.y)
  if (distance < 0.5) return null

  const controlX = (origin.x + gateway.x) / 2
  const controlY = Math.max(
    8,
    Math.min(origin.y, gateway.y) - Math.min(72, distance * 0.22)
  )
  return `M ${origin.x} ${origin.y} Q ${controlX} ${controlY} ${gateway.x} ${gateway.y}`
}

export function clampRequestOriginViewport(
  viewport: RequestOriginViewport
): RequestOriginViewport {
  return {
    ...viewport,
    x:
      viewport.width >= 1000
        ? (1000 - viewport.width) / 2
        : Math.max(0, Math.min(1000 - viewport.width, viewport.x)),
    y:
      viewport.height >= 500
        ? (500 - viewport.height) / 2
        : Math.max(0, Math.min(500 - viewport.height, viewport.y)),
  }
}

export function zoomRequestOriginViewport(
  viewport: RequestOriginViewport,
  factor: number,
  anchor: RequestOriginPosition = { x: 0.5, y: 0.5 }
): RequestOriginViewport {
  const aspect = viewport.width / viewport.height
  const width = Math.max(
    40,
    Math.min(Math.max(1000, 500 * aspect), viewport.width * factor)
  )
  const height = width / aspect
  return clampRequestOriginViewport({
    x: viewport.x + (viewport.width - width) * anchor.x,
    y: viewport.y + (viewport.height - height) * anchor.y,
    width,
    height,
  })
}
