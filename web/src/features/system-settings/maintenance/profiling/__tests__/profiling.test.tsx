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

import type { ProfileResult, ProfilingStatus } from '../api'
import { ProfilingPanel } from '../profiling-panel'

let client: QueryClient
const now = 1790208000000
const status: ProfilingStatus = {
  pyroscope_configured: true,
  pyroscope_running: true,
  pyroscope_available: true,
  app_name: 'new-api',
  profile_types: [
    {
      id: 'process_cpu:cpu:nanoseconds:cpu:nanoseconds',
      name: 'CPU',
      unit: 'nanoseconds',
    },
    {
      id: 'memory:inuse_space:bytes:space:bytes',
      name: 'Live memory',
      unit: 'bytes',
    },
  ],
  pprof_enabled: true,
  cpu_capture_available: false,
  capture_types: ['heap', 'allocs', 'goroutine', 'mutex', 'block'],
  max_capture_seconds: 15,
}
const profile: ProfileResult = {
  source: 'pyroscope',
  profile_type: status.profile_types[0].id,
  unit: 'nanoseconds',
  start: now - 3600000,
  end: now,
  total: 2000000,
  flamegraph: [
    { name: 'total', depth: 0, start: 0, total: 2000000, self: 0 },
    {
      name: 'gateway.relay',
      depth: 1,
      start: 0,
      total: 1500000,
      self: 1000000,
    },
    {
      name: 'runtime.work',
      depth: 1,
      start: 1500000,
      total: 500000,
      self: 500000,
    },
  ],
  hotspots: [
    { name: 'gateway.relay', self: 1000000, total: 1500000 },
    { name: 'runtime.work', self: 500000, total: 500000 },
  ],
  timeline: [],
  truncated: false,
}

beforeEach(async () => {
  localStorage.clear()
  await i18next.changeLanguage('en')
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'root', role: 100, quota: 0 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  vi.spyOn(Date, 'now').mockReturnValue(now)
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

function renderProfiles(currentStatus = status, currentProfile = profile) {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: currentStatus },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: currentProfile },
  })
  return render(
    <QueryClientProvider client={client}>
      <ProfilingPanel />
    </QueryClientProvider>
  )
}

it('does not fetch profiling data for users without root access', () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'admin', role: 10, quota: 0 })
  renderProfiles()
  expect(api.get).not.toHaveBeenCalled()
  expect(
    screen.queryByRole('region', { name: 'Performance profiling' })
  ).not.toBeInTheDocument()
})

it('shows configuration guidance without querying or automatically capturing', async () => {
  const user = userEvent.setup()
  renderProfiles({
    ...status,
    pyroscope_configured: false,
    pyroscope_available: false,
    pyroscope_running: false,
    pprof_enabled: false,
  })
  expect(
    await screen.findByText('Continuous profiling is not configured')
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Instant capture/ }))
  expect(
    screen.getByText(
      'Enable pprof on the server to collect profiles on demand.'
    )
  ).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
})

it('defaults to CPU regardless of discovery order and queries selected ranges within 24 hours', async () => {
  const user = userEvent.setup()
  renderProfiles({
    ...status,
    profile_types: [status.profile_types[1], status.profile_types[0]],
  })
  await screen.findByRole('button', { name: /Zoom into gateway.relay/ })
  expect(api.post).toHaveBeenCalledWith(
    '/api/performance/profiling/query',
    {
      profile_type: status.profile_types[0].id,
      start: now - 3600000,
      end: now,
    },
    expect.objectContaining({ signal: expect.any(AbortSignal) })
  )
  await user.selectOptions(screen.getByLabelText('Time range'), '1440')
  await waitFor(() =>
    expect(api.post).toHaveBeenLastCalledWith(
      '/api/performance/profiling/query',
      {
        profile_type: status.profile_types[0].id,
        start: now - 86400000,
        end: now,
      },
      expect.anything()
    )
  )
  await user.selectOptions(
    screen.getByLabelText('Profile type'),
    status.profile_types[1].id
  )
  await waitFor(() =>
    expect(api.post).toHaveBeenLastCalledWith(
      '/api/performance/profiling/query',
      {
        profile_type: status.profile_types[1].id,
        start: now - 86400000,
        end: now,
      },
      expect.anything()
    )
  )
})

it('supports keyboard flame graph zoom, reset, and ranked hotspot values', async () => {
  const user = userEvent.setup()
  renderProfiles()
  const frame = await screen.findByRole('button', {
    name: /Zoom into gateway.relay/,
  })
  frame.focus()
  await user.keyboard('{Enter}')
  expect(
    screen.getByText('Focused function', { exact: false })
  ).toHaveTextContent('gateway.relay')
  expect(
    screen.queryByRole('button', { name: /Zoom into runtime.work/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Reset view' }))
  expect(
    screen.getByRole('button', { name: /Zoom into runtime.work/ })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Function hotspots' }))
  const table = screen.getByRole('table', { name: 'Function hotspots' })
  const row = within(table).getByRole('row', { name: /gateway.relay/ })
  expect(within(row).getByText('1 ms')).toBeVisible()
  expect(within(row).getByText('1.5 ms')).toBeVisible()
  expect(within(row).getByText('50%')).toBeVisible()
})

it('refreshes the query end time instead of keeping the previous window', async () => {
  const user = userEvent.setup()
  renderProfiles()
  await screen.findByRole('button', { name: /Zoom into gateway.relay/ })
  vi.mocked(Date.now).mockReturnValue(now + 60000)
  await user.click(screen.getByRole('button', { name: 'Refresh profile' }))
  await waitFor(() =>
    expect(api.post).toHaveBeenLastCalledWith(
      '/api/performance/profiling/query',
      {
        profile_type: status.profile_types[0].id,
        start: now - 3540000,
        end: now + 60000,
      },
      expect.anything()
    )
  )
})

it('distinguishes empty samples from a failed query and allows retry', async () => {
  const user = userEvent.setup()
  renderProfiles()
  vi.mocked(api.post).mockRejectedValueOnce(
    new Error('Profile storage unavailable')
  )
  expect(await screen.findByText('Unable to load profiling data')).toBeVisible()
  expect(screen.queryByText('No profile samples')).not.toBeInTheDocument()
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: true,
      data: { ...profile, total: 0, flamegraph: [], hotspots: [] },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No profile samples')).toBeVisible()
})

it('keeps a business failure visible rather than displaying an empty successful profile', async () => {
  renderProfiles()
  vi.mocked(api.post).mockResolvedValue({
    data: { success: false, message: 'Profile access denied' },
  })
  expect(await screen.findByText('Profile access denied')).toBeVisible()
  expect(screen.queryByText('No profile samples')).not.toBeInTheDocument()
})

it('requires explicit capture and excludes CPU when continuous CPU collection is active', async () => {
  const user = userEvent.setup()
  renderProfiles({ ...status, pyroscope_configured: false })
  await user.click(await screen.findByRole('tab', { name: /Instant capture/ }))
  expect(api.post).not.toHaveBeenCalled()
  expect(screen.queryByRole('option', { name: 'CPU' })).not.toBeInTheDocument()
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: true,
      data: {
        ...profile,
        source: 'pprof',
        profile_type: 'heap',
        unit: 'bytes',
        download_id: 'capture-1',
      },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Collect profile' }))
  await screen.findByRole('button', { name: 'Download pprof' })
  expect(api.post).toHaveBeenCalledWith(
    '/api/performance/profiling/capture',
    { profile_type: 'heap', seconds: 5 },
    expect.objectContaining({ timeout: 30000 })
  )
})

it('locks capture controls while sampling and cancels an unfinished CPU request on exit', async () => {
  const user = userEvent.setup()
  const view = renderProfiles({
    ...status,
    pyroscope_configured: false,
    cpu_capture_available: true,
    capture_types: ['cpu', 'heap'],
    max_capture_seconds: 5,
  })
  await user.click(await screen.findByRole('tab', { name: /Instant capture/ }))
  await user.selectOptions(screen.getByLabelText('Profile type'), 'cpu')
  expect(screen.getByLabelText('Capture duration')).toHaveValue('5')
  expect(
    screen.queryByRole('option', { name: '15 seconds' })
  ).not.toBeInTheDocument()
  vi.mocked(api.post).mockImplementation(() => new Promise(() => {}))
  await user.click(screen.getByRole('button', { name: 'Collect profile' }))
  expect(screen.getByLabelText('Profile type')).toBeDisabled()
  expect(screen.getByLabelText('Capture duration')).toBeDisabled()
  expect(screen.getByRole('status')).toHaveTextContent('Collecting profile...')
  const signal = vi.mocked(api.post).mock.calls.at(-1)?.[2]
    ?.signal as AbortSignal
  view.unmount()
  expect(signal.aborted).toBe(true)
})

it('shows capture conflicts instead of retaining the preceding successful snapshot', async () => {
  const user = userEvent.setup()
  renderProfiles({ ...status, pyroscope_configured: false })
  await user.click(await screen.findByRole('tab', { name: /Instant capture/ }))
  vi.mocked(api.post).mockRejectedValue(
    new Error('A profile capture is already running')
  )
  await user.click(screen.getByRole('button', { name: 'Collect profile' }))
  expect(
    await screen.findByText('A profile capture is already running')
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Download pprof' })
  ).not.toBeInTheDocument()
})

it('downloads the authenticated pprof endpoint and revokes the temporary URL', async () => {
  const user = userEvent.setup()
  renderProfiles({ ...status, pyroscope_configured: false })
  await user.click(await screen.findByRole('tab', { name: /Instant capture/ }))
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: true,
      data: { ...profile, source: 'pprof', download_id: 'sample/id' },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Collect profile' }))
  const blob = new Blob(['profile-data'], { type: 'application/octet-stream' })
  vi.mocked(api.get).mockResolvedValue({ data: blob })
  const createObjectURL = vi.fn(() => 'blob:profile-download')
  const revokeObjectURL = vi.fn()
  const BrowserURL = URL
  vi.stubGlobal(
    'URL',
    class extends BrowserURL {
      static createObjectURL = createObjectURL
      static revokeObjectURL = revokeObjectURL
    }
  )
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  await user.click(
    await screen.findByRole('button', { name: 'Download pprof' })
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/performance/profiling/profiles/sample%2Fid',
      expect.objectContaining({
        responseType: 'blob',
        signal: expect.any(AbortSignal),
      })
    )
  )
  expect(createObjectURL).toHaveBeenCalledWith(blob)
  expect(revokeObjectURL).toHaveBeenCalledWith('blob:profile-download')
})

it('translates an expired download returned as a JSON error Blob without showing the raw body', async () => {
  const user = userEvent.setup()
  renderProfiles({ ...status, pyroscope_configured: false })
  await user.click(await screen.findByRole('tab', { name: /Instant capture/ }))
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: true,
      data: { ...profile, source: 'pprof', download_id: 'expired-sample' },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Collect profile' }))
  await screen.findByRole('button', { name: 'Download pprof' })
  i18next.addResourceBundle('zhCN', 'translation', {
    'Profile download has expired': '性能数据下载已过期',
  })
  await act(() => i18next.changeLanguage('zhCN'))
  const raw = JSON.stringify({
    success: false,
    message: 'Profile download has expired',
    internal: 'private error details',
  })
  const body = new Blob([raw], { type: 'application/json' })
  // jsdom lacks Blob.text; model only this browser boundary.
  Object.defineProperty(body, 'text', { value: async () => raw })
  vi.mocked(api.get).mockRejectedValue({
    isAxiosError: true,
    response: { status: 404, data: body },
  })
  try {
    await user.click(screen.getByRole('button', { name: 'Download pprof' }))
    expect(await screen.findByText('性能数据下载已过期')).toBeVisible()
    expect(
      screen.queryByText('private error details', { exact: false })
    ).not.toBeInTheDocument()
  } finally {
    i18next.removeResourceBundle('zhCN', 'translation')
    await act(() => i18next.changeLanguage('en'))
  }
})

it('marks truncated profiles and clears privileged results when root access is removed', async () => {
  renderProfiles(status, { ...profile, truncated: true })
  expect(
    await screen.findByText(
      'This profile exceeds the display limit. Only part of the call stack and the top 100 functions are shown.'
    )
  ).toBeVisible()
  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'admin', role: 10, quota: 0 })
  )
  expect(
    screen.queryByRole('region', { name: 'Performance profiling' })
  ).not.toBeInTheDocument()
})

it('updates profile numbers when interface language changes including legacy Chinese codes', async () => {
  renderProfiles(status, { ...profile, total: 1234567890 })
  await screen.findByRole('button', { name: /Zoom into gateway.relay/ })
  for (const language of ['zhCN', 'zhTW', 'fr', 'ru', 'ja', 'vi']) {
    i18next.addResourceBundle(language, 'translation', {
      'Profile total': 'Profile total',
    })
  }
  for (const [language, value] of [
    ['zhCN', '1.23'],
    ['zhTW', '1.23'],
    ['en', '1.23'],
    ['fr', '1,23'],
    ['ru', '1,23'],
    ['ja', '1.23'],
    ['vi', '1,23'],
    ['invalid-language', '1.23'],
  ]) {
    await act(() => i18next.changeLanguage(language))
    expect(
      screen.getByText(`${i18next.t('Profile total')}: ${value} s`)
    ).toBeVisible()
  }
  for (const language of ['zhCN', 'zhTW', 'fr', 'ru', 'ja', 'vi']) {
    i18next.removeResourceBundle(language, 'translation')
  }
})
