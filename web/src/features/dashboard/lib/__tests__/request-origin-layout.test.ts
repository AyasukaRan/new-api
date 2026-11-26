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
import { describe, expect, it } from 'vitest'

import {
  calculateRequestOriginLayout,
  clampRequestOriginViewport,
  createRequestOriginRoute,
  projectRequestOrigin,
  REQUEST_ORIGIN_GATEWAY,
  zoomRequestOriginViewport,
} from '../request-origin-layout'

describe('request origin map layout', () => {
  it('projects sources onto the Asia-centered world geometry with wrapped longitudes', () => {
    expect(projectRequestOrigin({ longitude: -70, latitude: 90 })).toEqual({
      x: 0,
      y: 0,
    })
    expect(projectRequestOrigin({ longitude: 110, latitude: 0 })).toEqual({
      x: 500,
      y: 250,
    })
    const westCoast = projectRequestOrigin({
      longitude: -122.3996,
      latitude: 37.784263,
    })
    expect(westCoast.x).toBeCloseTo(854.4456, 4)
    expect(westCoast.y).toBeCloseTo(145.0437, 4)
    const gateway = projectRequestOrigin(REQUEST_ORIGIN_GATEWAY)
    expect(gateway.x).toBeCloseTo(502.1667, 4)
    expect(gateway.y).toBeCloseTo(159.5277, 4)
  })

  it('zooms to China when every source is domestic and keeps the gateway visible', () => {
    const layout = calculateRequestOriginLayout(
      ['CN', 'HK', 'MO', 'TW'].map((country_code) => ({ country_code }))
    )
    expect(layout.mode).toBe('china')
    expect(layout.width).toBeLessThan(1000)
    expect(layout.scale).toBeLessThan(1)
    const [left, top, width, height] = layout.viewBox.split(' ').map(Number)
    for (const point of [
      { longitude: 70, latitude: 56 },
      { longitude: 138, latitude: 15 },
      REQUEST_ORIGIN_GATEWAY,
    ]) {
      const position = projectRequestOrigin(point)
      expect(position.x).toBeGreaterThan(left)
      expect(position.x).toBeLessThan(left + width)
      expect(position.y).toBeGreaterThan(top)
      expect(position.y).toBeLessThan(top + height)
    }
  })

  it.each([
    { countries: [] },
    { countries: ['CN', 'US'] },
    { countries: ['CN', ''] },
    { countries: ['CN', 'ZZ'] },
  ])(
    'keeps a world view for empty, foreign, or unknown source countries: $countries',
    ({ countries }) => {
      const layout = calculateRequestOriginLayout(
        countries.map((country_code) => ({ country_code }))
      )
      expect(layout.mode).toBe('world')
      expect(layout.viewBox).toBe('0 0 1000 500')
      expect(layout.scale).toBe(1)
    }
  )

  it('keeps the world view when a foreign source is outside the top 500', () => {
    const points = Array.from({ length: 500 }, () => ({ country_code: 'CN' }))
    points.push({ country_code: 'US' })
    expect(calculateRequestOriginLayout(points).mode).toBe('world')
  })

  it('connects each source to Shiyan with a finite curved path', () => {
    for (const point of [
      { longitude: 121.434559, latitude: 31.218398 },
      { longitude: -122.3996, latitude: 37.784263 },
    ]) {
      const route = createRequestOriginRoute(point)
      expect(route).not.toBeNull()
      if (!route) throw new Error('Expected a route to Shiyan')
      const coordinates = route
        .split(/[MQ\s]+/)
        .filter(Boolean)
        .map(Number)
      expect(coordinates.every(Number.isFinite)).toBe(true)
      const source = projectRequestOrigin(point)
      const gateway = projectRequestOrigin(REQUEST_ORIGIN_GATEWAY)
      expect(coordinates.slice(0, 2)).toEqual([source.x, source.y])
      expect(coordinates.slice(-2)).toEqual([gateway.x, gateway.y])
      expect(coordinates[3]).toBeLessThan(Math.min(source.y, gateway.y))
    }
  })

  it('omits a flight when the source is already at the gateway', () => {
    expect(createRequestOriginRoute(REQUEST_ORIGIN_GATEWAY)).toBeNull()
  })

  it('keeps the wheel anchor fixed while bounding zoom from city detail to the whole world', () => {
    const viewport = { x: 300, y: 80, width: 300, height: 150 }
    const zoomed = zoomRequestOriginViewport(viewport, 0.5, {
      x: 0.75,
      y: 0.25,
    })
    expect(zoomed.x + zoomed.width * 0.75).toBe(525)
    expect(zoomed.y + zoomed.height * 0.25).toBe(117.5)
    expect(zoomRequestOriginViewport(viewport, 100)).toEqual({
      x: 0,
      y: 0,
      width: 1000,
      height: 500,
    })
    expect(zoomRequestOriginViewport(viewport, 0.001).width).toBe(40)
  })

  it('keeps a dragged viewport inside the world boundaries', () => {
    expect(
      clampRequestOriginViewport({ x: -100, y: 480, width: 300, height: 150 })
    ).toEqual({ x: 0, y: 350, width: 300, height: 150 })
  })

  it.each([
    {
      viewport: { x: 300, y: 80, width: 300, height: 100 },
      expected: { x: -250, y: 0, width: 1500, height: 500 },
    },
    {
      viewport: { x: 300, y: 80, width: 200, height: 200 },
      expected: { x: 0, y: -250, width: 1000, height: 1000 },
    },
  ])(
    'fits the whole world in a non-2:1 viewport without cropping an axis',
    ({ viewport, expected }) => {
      expect(zoomRequestOriginViewport(viewport, 100)).toEqual(expected)
      expect(
        clampRequestOriginViewport({ ...expected, x: -999, y: 999 })
      ).toEqual(expected)
    }
  )
})
