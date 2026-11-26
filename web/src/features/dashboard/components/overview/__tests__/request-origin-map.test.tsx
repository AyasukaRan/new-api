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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type {
  RequestOriginPoint,
  RequestOriginsSnapshot,
} from '../../../lib/request-origins-api'
import { RequestOriginMap } from '../request-origin-map'

let client: QueryClient
let streams: ReadableStreamDefaultController<Uint8Array>[]
const encoder = new TextEncoder()
const point: RequestOriginPoint = {
  location_id: 'tokyo',
  country_code: 'JP',
  country: 'Japan',
  region: 'Tokyo',
  city: 'Tokyo',
  latitude: 35.68,
  longitude: 139.69,
  count: 7,
  last_seen: 100,
}
const snapshot: RequestOriginsSnapshot = {
  points: [point],
  total: 10,
  located: 7,
  unmapped: 3,
  private: 2,
  invalid: 1,
  truncated: false,
  sample_start: 100,
  cursor: 'snapshot-1',
  geoip_available: true,
  geoip_updated_at: 100,
}
const beijing: RequestOriginPoint = {
  ...point,
  location_id: 'beijing',
  country_code: 'CN',
  country: 'China',
  region: 'Beijing',
  city: 'Beijing',
  latitude: 39.9,
  longitude: 116.4,
}
const shanghai: RequestOriginPoint = {
  ...beijing,
  location_id: 'shanghai',
  city: 'Shanghai',
  region: 'Shanghai',
  longitude: 121.47,
  latitude: 31.23,
  count: 28,
}

beforeEach(async () => {
  await i18next.changeLanguage('en')
  useAuthStore
    .getState()
    .auth.setUser({ id: 7, username: 'member', role: 1, quota: 0 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  streams = []
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
})

afterEach(async () => {
  cleanup()
  client.clear()
  vi.useRealTimers()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  await i18next.changeLanguage('en')
})

function mockOrigins(data = snapshot) {
  return vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (String(url).endsWith('/stream')) {
      return {
        data: new ReadableStream<Uint8Array>({
          start(controller) {
            streams.push(controller)
            config?.signal?.addEventListener?.('abort', () => {
              try {
                controller.close()
              } catch {
                /* Already closed by test. */
              }
            })
            controller.enqueue(encoder.encode('event: ready\ndata: {}\n\n'))
          },
        }),
      }
    }
    return { data: { success: true, data } }
  })
}

function renderMap(props: Parameters<typeof RequestOriginMap>[0] = {}) {
  return render(
    <QueryClientProvider client={client}>
      <RequestOriginMap {...props} />
    </QueryClientProvider>
  )
}

it('uses the personal endpoint and omits a forged user filter for an ordinary member', async () => {
  const get = mockOrigins()
  renderMap({
    params: {
      start_timestamp: 50,
      end_timestamp: 200,
      username: 'someone-else',
    },
  })
  const map = await screen.findByRole('group', {
    name: 'Request origin world map',
  })
  expect(
    within(map).getByRole('button', {
      name: 'Tokyo, Japan · 7 request records',
    })
  ).toBeInTheDocument()
  expect(get).toHaveBeenCalledWith(
    '/api/data/origins/self',
    expect.objectContaining({
      params: { start_timestamp: 50, end_timestamp: 200 },
    })
  )
  expect(screen.getByText('Only mine')).toBeInTheDocument()
  expect(screen.getByText('Historical range')).toBeInTheDocument()
  expect(streams).toHaveLength(0)
  expect(screen.getByText('Unlocated').parentElement).toHaveTextContent('3')
})

it('retains the admin user and time filter and lets keyboard users inspect a location', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: 10, quota: 0 })
  const get = mockOrigins()
  renderMap({
    params: { start_timestamp: 50, end_timestamp: 200, username: 'member' },
  })
  const map = await screen.findByRole('group', {
    name: 'Request origin world map',
  })
  expect(get).toHaveBeenCalledWith(
    '/api/data/origins',
    expect.objectContaining({
      params: { start_timestamp: 50, end_timestamp: 200, username: 'member' },
    })
  )
  const marker = within(map).getByRole('button', {
    name: 'Tokyo, Japan · 7 request records',
  })
  marker.focus()
  await userEvent.keyboard('{Enter}')
  expect(marker).toHaveAttribute('aria-pressed', 'true')
  expect(
    screen.getByText('Tokyo, Japan', { selector: 'span' })
  ).toBeInTheDocument()
})

it('animates only newly streamed locations without counting a snapshot or replay twice', async () => {
  const get = mockOrigins()
  const view = renderMap()
  await screen.findByText('Live')
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  expect(
    view.container.querySelector('[data-origin-route="tokyo"]')
  ).toBeInTheDocument()
  const next = { ...point, last_seen: 101, count: 1 }
  await act(async () => {
    streams[0].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'next', points: [next] })}\n\n`
      )
    )
  })
  expect(view.container.querySelectorAll('[data-origin-pulse]')).toHaveLength(1)
  expect(
    view.container.querySelector<SVGCircleElement>('.origin-map-flight-head')
      ?.style.offsetPath
  ).toMatch(/^path\("M /)
  await act(async () => {
    streams[0].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'next', points: [next] })}\n\n`
      )
    )
  })
  expect(view.container.querySelectorAll('[data-origin-pulse]')).toHaveLength(1)
  expect(screen.getByText('Request records').parentElement).toHaveTextContent(
    '10'
  )
  expect(
    get.mock.calls.find(([url]) => String(url).endsWith('/stream'))?.[1]?.params
  ).not.toHaveProperty('end_timestamp')
  await userEvent.click(screen.getByRole('button', { name: 'Pause live map' }))
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  expect(screen.getByText('Paused')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Resume live map' }))
  await screen.findByText('Live')
  expect(streams).toHaveLength(2)
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  await act(async () => {
    streams[1].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'next', points: [next] })}\n\n`
      )
    )
  })
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  await act(async () => {
    streams[1].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'after-resume', points: [{ ...point, last_seen: 102 }] })}\n\n`
      )
    )
  })
  expect(view.container.querySelectorAll('[data-origin-pulse]')).toHaveLength(1)
  expect(
    view.container.querySelector('[data-origin-pulse="tokyo:102"]')
  ).toBeInTheDocument()
})

it.each([{ points: [beijing] }, { points: [beijing, shanghai] }])(
  'zooms domestic sources and retains request counts and keyboard selection',
  async ({ points }) => {
    mockOrigins({ ...snapshot, points })
    const view = renderMap({
      params: { start_timestamp: 50, end_timestamp: 200 },
    })
    const map = await screen.findByRole('group', {
      name: 'Request origin China map',
    })
    expect(map).toHaveAttribute('data-origin-map-mode', 'china')
    expect(map).not.toHaveAttribute('viewBox', '0 0 1000 500')
    expect(within(map).getByText('Shiyan, Hubei · Gateway')).toBeInTheDocument()
    expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
    const marker = within(map).getByRole('button', {
      name: 'Beijing, China · 7 request records',
    })
    marker.focus()
    await userEvent.keyboard(' ')
    expect(marker).toHaveAttribute('aria-pressed', 'true')
    expect(
      screen.getByText('7 request records', { selector: 'strong' })
    ).toBeInTheDocument()
    expect(
      screen.getByText('Marker size and route width show request counts.')
    ).toBeInTheDocument()
  }
)

it('uses larger markers and heavier routes for greater snapshot request counts', async () => {
  mockOrigins({
    ...snapshot,
    points: [beijing, shanghai],
  })
  const view = renderMap({
    params: { start_timestamp: 50, end_timestamp: 200 },
  })
  await screen.findByRole('group', { name: 'Request origin China map' })
  const smallerMarker = view.container.querySelector(
    '[data-origin-location="beijing"] .origin-map-dot'
  )
  const largerMarker = view.container.querySelector(
    '[data-origin-location="shanghai"] .origin-map-dot'
  )
  expect(Number(largerMarker?.getAttribute('r'))).toBeGreaterThan(
    Number(smallerMarker?.getAttribute('r'))
  )
  const smallerRoute = view.container.querySelector(
    '[data-origin-route="beijing"]'
  )
  const largerRoute = view.container.querySelector(
    '[data-origin-route="shanghai"]'
  )
  expect(Number(largerRoute?.getAttribute('stroke-width'))).toBeGreaterThan(
    Number(smallerRoute?.getAttribute('stroke-width'))
  )
})

it('keeps the world view when an overseas location falls outside the displayed top 500', async () => {
  const domestic = Array.from({ length: 500 }, (_, index) => ({
    ...beijing,
    location_id: `domestic-${index}`,
    count: 8,
  }))
  mockOrigins({ ...snapshot, points: [...domestic, point] })
  const view = renderMap({
    params: { start_timestamp: 50, end_timestamp: 200 },
  })
  const map = await screen.findByRole('group', {
    name: 'Request origin world map',
  })
  expect(map).toHaveAttribute('viewBox', '0 0 1000 500')
  expect(within(map).getAllByRole('button')).toHaveLength(500)
  expect(
    view.container.querySelector('[data-origin-location="tokyo"]')
  ).toBeNull()
})

it('keeps the world view for a mapped source with an unknown country', async () => {
  mockOrigins({
    ...snapshot,
    points: [beijing, { ...point, country_code: '', country: '' }],
  })
  renderMap({ params: { start_timestamp: 50, end_timestamp: 200 } })
  expect(
    await screen.findByRole('group', { name: 'Request origin world map' })
  ).toHaveAttribute('data-origin-map-mode', 'world')
})

it('shows a new overseas request immediately and keeps the world view until the snapshot catches up', async () => {
  mockOrigins({ ...snapshot, points: [beijing] })
  const view = renderMap()
  await screen.findByText('Live')
  expect(
    screen.getByRole('group', { name: 'Request origin China map' })
  ).toBeInTheDocument()
  vi.useFakeTimers()
  await act(async () => {
    streams[0].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'overseas', points: [{ ...point, last_seen: 101 }] })}\n\n`
      )
    )
  })
  expect(
    screen.getByRole('group', { name: 'Request origin world map' })
  ).toBeInTheDocument()
  expect(
    view.container.querySelector('[data-origin-pulse="tokyo:101"]')
  ).toBeInTheDocument()
  expect(screen.getByText('Request records').parentElement).toHaveTextContent(
    '10'
  )
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000)
  })
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  expect(
    screen.getByRole('group', { name: 'Request origin world map' })
  ).toBeInTheDocument()
  expect(screen.getByText('Located').parentElement).toHaveTextContent('7')
})

it('keeps the gateway readable and shows only the selected source label on a narrow map', async () => {
  vi.spyOn(SVGElement.prototype, 'getBoundingClientRect').mockReturnValue({
    width: 360,
    height: 180,
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 360,
    bottom: 180,
    toJSON: () => ({}),
  })
  mockOrigins({ ...snapshot, points: [beijing, shanghai] })
  const view = renderMap({
    params: { start_timestamp: 50, end_timestamp: 200 },
  })
  const map = await screen.findByRole('group', {
    name: 'Request origin China map',
  })
  expect(within(map).getByText('Shiyan, Hubei · Gateway')).toBeInTheDocument()
  expect(
    view.container.querySelectorAll('.origin-map-location-label')
  ).toHaveLength(0)
  await userEvent.click(
    within(map).getByRole('button', {
      name: 'Beijing, China · 7 request records',
    })
  )
  expect(
    view.container.querySelectorAll('.origin-map-location-label')
  ).toHaveLength(1)
  expect(
    view.container.querySelector('.origin-map-location-label')
  ).toHaveTextContent('Beijing · 7')
})

it('keeps real location counts but suppresses pulse motion when reduced motion is requested', async () => {
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    matches: query.includes('prefers-reduced-motion'),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
  mockOrigins()
  const view = renderMap()
  await screen.findByText('Live')
  await act(async () => {
    streams[0].enqueue(
      encoder.encode(
        `event: origins\ndata: ${JSON.stringify({ cursor: 'next', points: [{ ...point, last_seen: 101 }] })}\n\n`
      )
    )
  })
  expect(view.container.querySelector('[data-origin-pulse]')).toBeNull()
  expect(screen.getByText('Located').parentElement).toHaveTextContent('7')
})

it('shows an unavailable database without inventing a location or opening a stream', async () => {
  mockOrigins({
    ...snapshot,
    points: [],
    located: 0,
    unmapped: 10,
    geoip_available: false,
  })
  renderMap()
  expect(
    await screen.findByText('GeoIP database unavailable')
  ).toBeInTheDocument()
  expect(streams).toHaveLength(0)
  expect(
    screen.getByRole('link', { name: 'IP Geolocation by DB-IP' })
  ).toHaveAttribute('href', 'https://db-ip.com')
})

it('shows load failures and retries without dropping the selected historical range', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue({ data: { success: true, data: snapshot } })
  renderMap({ params: { start_timestamp: 50, end_timestamp: 200 } })
  expect(
    await screen.findByText('Failed to load request origins')
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('group', { name: 'Request origin world map' })
  ).toBeInTheDocument()
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
})

it('rejects invalid coordinates instead of drawing an incorrect marker', async () => {
  mockOrigins({ ...snapshot, points: [{ ...point, latitude: 100 }] })
  renderMap()
  expect(
    await screen.findByText('Failed to load request origins')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('group', { name: 'Request origin world map' })
  ).not.toBeInTheDocument()
})

it('keeps a live range connected across unrelated rerenders but stops for a historical range', async () => {
  const get = mockOrigins()
  const now = Math.floor(Date.now() / 1000)
  const params = { start_timestamp: now - 86400, end_timestamp: now }
  const view = renderMap({ params })
  await screen.findByText('Live')
  expect(streams).toHaveLength(1)
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime((now + 180) * 1000)
  view.rerender(
    <QueryClientProvider client={client}>
      <RequestOriginMap params={{ ...params }} />
    </QueryClientProvider>
  )
  expect(screen.getByText('Live')).toBeInTheDocument()
  expect(streams).toHaveLength(1)
  view.rerender(
    <QueryClientProvider client={client}>
      <RequestOriginMap params={{ ...params, end_timestamp: now - 3600 }} />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Historical range')).toBeInTheDocument()
  expect(
    get.mock.calls.filter(([url]) => String(url).endsWith('/stream'))
  ).toHaveLength(1)
})
