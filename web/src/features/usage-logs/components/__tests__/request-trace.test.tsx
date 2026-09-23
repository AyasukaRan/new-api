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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
  vi,
} from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { RequestTraceLeg } from '../../types'
import { RequestTraceSection } from '../dialogs/request-trace-section'

const getRequestTrace = vi.hoisted(() => vi.fn())
const getRequestTraceObject = vi.hoisted(() => vi.fn())

function makeLeg(leg: Partial<RequestTraceLeg>): RequestTraceLeg {
  return {
    seq: 0,
    attempt: 0,
    direction: 'client_request',
    channel_id: 0,
    format: 'openai',
    method: 'POST',
    url: '',
    status: 0,
    headers: null,
    body: '',
    body_size: 0,
    truncated: false,
    has_object: false,
    ...leg,
  }
}

const requestBody = JSON.stringify({
  messages: [{ role: 'user', content: 'Hello there' }],
})
const reply = makeLeg({
  seq: 5,
  direction: 'client_response',
  status: 200,
  rendered: { content: 'Final client reply', stream: false },
})

async function openTrace() {
  await userEvent.click(
    screen.getByRole('button', { name: 'View request trace' })
  )
}

async function openExchanges() {
  await userEvent.click(
    await screen.findByRole('tab', { name: 'Exchange details' })
  )
  return screen.getByRole('tablist', { name: 'Request exchanges' })
}

describe('request trace viewer', () => {
  const queryClients: QueryClient[] = []

  function renderSection(enabled = true) {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    queryClients.push(queryClient)
    return render(
      <QueryClientProvider client={queryClient}>
        <RequestTraceSection traceId='trace-1' enabled={enabled} />
      </QueryClientProvider>
    )
  }

  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      'Attempt {{index}}': 'Attempt {{index}}',
      'Only the beginning and the end were kept. The original payload was {{bytes}} bytes.':
        'Only the beginning and the end were kept. The original payload was {{bytes}} bytes.',
    })
  })
  beforeEach(() => {
    getRequestTrace.mockReset()
    getRequestTraceObject.mockReset()
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
      if (url === '/api/log/trace/object') {
        return {
          data: await getRequestTraceObject(
            config?.params?.trace_id,
            config?.params?.seq
          ),
        }
      }
      return {
        data: {
          success: true,
          data: {
            trace_id: config?.params?.trace_id,
            legs: await getRequestTrace(config?.params?.trace_id),
          },
        },
      }
    })
  })
  afterEach(() => {
    for (const client of queryClients) client.clear()
    queryClients.length = 0
    useAuthStore.getState().auth.reset()
  })

  test('loads the trace only after opening its independent dialog', async () => {
    getRequestTrace.mockResolvedValue([makeLeg({ body: requestBody }), reply])
    renderSection()
    expect(getRequestTrace).not.toHaveBeenCalled()
    await openTrace()
    expect(
      await screen.findByRole('dialog', { name: 'Request Trace' })
    ).toBeInTheDocument()
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Conversation' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(getRequestTrace).toHaveBeenCalledWith('trace-1')
  })

  test('keeps both client legs reachable while switching between upstream retries', async () => {
    getRequestTrace.mockResolvedValue([
      makeLeg({ body: requestBody }),
      makeLeg({
        seq: 1,
        direction: 'upstream_request',
        channel_id: 12,
        body: '{"attempt":"first"}',
      }),
      makeLeg({
        seq: 2,
        direction: 'upstream_response',
        channel_id: 12,
        status: 503,
        rendered: { content: 'Service unavailable', stream: false },
      }),
      makeLeg({
        seq: 3,
        direction: 'upstream_request',
        attempt: 1,
        channel_id: 25,
        body: '{"attempt":"second"}',
      }),
      makeLeg({
        seq: 4,
        direction: 'upstream_response',
        attempt: 1,
        channel_id: 25,
        status: 200,
        rendered: { content: 'Upstream reply', stream: false },
      }),
      reply,
    ])
    renderSection()
    await openTrace()
    const nav = await openExchanges()
    expect(
      within(nav).getByRole('tab', { name: 'new-api → Client' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Upstream reply')).not.toBeInTheDocument()
    await userEvent.click(
      within(nav).getByRole('tab', { name: 'Upstream → new-api · Attempt 1' })
    )
    expect(await screen.findByText('HTTP 503')).toBeInTheDocument()
    expect(screen.getByText('Service unavailable')).toBeInTheDocument()
    await userEvent.click(
      within(nav).getByRole('tab', { name: 'Upstream → new-api · Attempt 2' })
    )
    expect(await screen.findByText('Upstream reply')).toBeInTheDocument()
    expect(screen.queryByText('Service unavailable')).not.toBeInTheDocument()
    expect(
      within(nav).getByRole('tab', { name: 'Client → new-api' })
    ).toBeEnabled()
    await userEvent.click(
      within(nav).getByRole('tab', { name: 'new-api → Client' })
    )
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
  })

  test('selects the last upstream response when the client response was not captured', async () => {
    getRequestTrace.mockResolvedValue([
      makeLeg({
        seq: 1,
        direction: 'upstream_response',
        rendered: { content: 'Earlier attempt', stream: false },
      }),
      makeLeg({
        seq: 2,
        attempt: 1,
        direction: 'upstream_response',
        rendered: { content: 'Last attempt', stream: false },
      }),
    ])
    renderSection()
    await openTrace()
    const nav = await openExchanges()
    expect(
      within(nav).getByRole('tab', { name: 'Upstream → new-api · Attempt 2' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Earlier attempt')).not.toBeInTheDocument()
  })

  test('keeps payload view selection separate from exchange navigation', async () => {
    getRequestTrace.mockResolvedValue([reply])
    renderSection()
    await openTrace()
    const exchanges = await openExchanges()
    expect(exchanges).toHaveAttribute('aria-orientation', 'vertical')
    const payloadViews = screen.getByRole('tablist', { name: 'Payload views' })
    expect(payloadViews).toHaveAttribute('data-orientation', 'horizontal')
    // Nested horizontal tabs must style their own orientation, not inherit
    // the vertical exchange navigator's direction through an ancestor group.
    expect(payloadViews).toHaveClass('data-vertical:flex-col')
    expect(payloadViews).not.toHaveClass('group-data-vertical/tabs:flex-col')
    await userEvent.click(screen.getByRole('tab', { name: 'Headers' }))
    expect(
      await screen.findByText('No headers were captured for this exchange.')
    ).toBeInTheDocument()
    expect(
      screen.getByRole('tab', { name: 'new-api → Client' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Final client reply')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Rendered' }))
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
  })

  test('supports arrow-key navigation with the selected exchange exposed accessibly', async () => {
    const user = userEvent.setup()
    getRequestTrace.mockResolvedValue([makeLeg({ body: requestBody }), reply])
    renderSection()
    await openTrace()
    const nav = await openExchanges()
    const client = within(nav).getByRole('tab', { name: 'new-api → Client' })
    client.focus()
    await user.keyboard('{ArrowUp}')
    const input = within(nav).getByRole('tab', { name: 'Client → new-api' })
    await waitFor(() => expect(input).toHaveFocus())
    await user.keyboard('{Enter}')
    expect(input).toHaveAttribute('aria-selected', 'true')
  })

  test('switches exchange navigation to a horizontal strip on narrow screens', async () => {
    const originalMatchMedia = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...originalMatchMedia(query),
      matches: query === '(max-width: 767px)',
    }))
    const scrollIntoView = vi.spyOn(HTMLElement.prototype, 'scrollIntoView')
    getRequestTrace.mockResolvedValue([makeLeg({ body: requestBody }), reply])
    renderSection()
    await openTrace()
    const nav = await openExchanges()
    expect(scrollIntoView.mock.contexts).toContain(
      within(nav).getByRole('tab', { name: 'new-api → Client' })
    )
    expect(nav).toHaveAttribute('aria-orientation', 'horizontal')
    expect(nav.parentElement).toHaveClass('overflow-auto')
    expect(nav).toHaveClass('data-horizontal:h-auto')
  })

  test('preserves truncation warnings even when displaying the parsed reply', async () => {
    getRequestTrace.mockResolvedValue([
      { ...reply, truncated: true, body_size: 999999 },
    ])
    renderSection()
    await openTrace()
    await openExchanges()
    expect(
      screen.getByText(
        'Only the beginning and the end were kept. The original payload was 999999 bytes.'
      )
    ).toBeInTheDocument()
    expect(screen.getByText('Final client reply')).toBeInTheDocument()
  })

  test('offers a refresh and a copyable trace ID when no trace records are available', async () => {
    getRequestTrace.mockResolvedValueOnce([]).mockResolvedValueOnce([reply])
    renderSection()
    await openTrace()
    expect(
      await screen.findByText('No trace records are available.')
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Copy trace ID' }))
    expect(await navigator.clipboard.readText()).toBe('trace-1')
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
  })

  test.each([
    { success: false, message: 'Trace storage unavailable' },
    { success: true },
    { success: true, data: { legs: null } },
    { success: true, data: { legs: {} } },
  ])(
    'reports unsuccessful or malformed responses as retryable errors: %j',
    async (data) => {
      vi.mocked(api.get).mockResolvedValue({ data })
      renderSection()
      await openTrace()
      expect(
        await screen.findByText('Failed to load the request trace')
      ).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
      expect(
        screen.getByRole('button', { name: 'Copy trace ID' })
      ).toBeEnabled()
      expect(
        screen.queryByText('No trace records are available.')
      ).not.toBeInTheDocument()
      expect(
        screen.queryByText('This trace has expired or was never recorded.')
      ).not.toBeInTheDocument()
    }
  )

  test('allows retrying a failed trace load', async () => {
    getRequestTrace
      .mockRejectedValueOnce(new Error('Unavailable'))
      .mockResolvedValueOnce([reply])
    renderSection()
    await openTrace()
    expect(
      await screen.findByText('Failed to load the request trace')
    ).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
  })

  test('loads stored media only when its body is selected and reports retrieval failure', async () => {
    getRequestTraceObject.mockRejectedValue(new Error('Expired object'))
    getRequestTrace.mockResolvedValue([
      makeLeg({
        has_object: true,
        content_type: 'audio/mpeg',
        body_size: 2048,
      }),
      reply,
    ])
    renderSection()
    await openTrace()
    const nav = await openExchanges()
    expect(getRequestTraceObject).not.toHaveBeenCalled()
    await userEvent.click(
      within(nav).getByRole('tab', { name: 'Client → new-api' })
    )
    expect(
      await screen.findByText(
        'The stored payload could not be loaded. It may have expired.'
      )
    ).toBeInTheDocument()
    expect(
      screen.queryByText('No body was captured for this leg.')
    ).not.toBeInTheDocument()
    expect(getRequestTraceObject).toHaveBeenCalledWith('trace-1', 0)
  })

  test('returns focus to the launcher after closing and retrieves private trace data when reopened', async () => {
    const user = userEvent.setup()
    getRequestTrace.mockResolvedValue([reply])
    renderSection()
    await openTrace()
    await screen.findByText('Final client reply')
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(
      screen.getByRole('button', { name: 'View request trace' })
    ).toHaveFocus()
    await openTrace()
    expect(await screen.findByText('Final client reply')).toBeInTheDocument()
    expect(getRequestTrace).toHaveBeenCalledTimes(2)
  })

  test('does not offer a trace to a non-administrator', () => {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'member', role: 1 })
    renderSection()
    expect(
      screen.queryByRole('button', { name: 'View request trace' })
    ).not.toBeInTheDocument()
    expect(api.get).not.toHaveBeenCalled()
  })

  test('cancels an in-flight trace and drops its data when the account changes', async () => {
    let complete: (value: unknown) => void = () => undefined
    let requestSignal: { aborted: boolean } | undefined
    vi.mocked(api.get).mockImplementation((_url, config) => {
      requestSignal = config?.signal
      return new Promise((resolve) => {
        complete = resolve
      })
    })
    renderSection()
    await openTrace()
    await screen.findByRole('status', { name: 'Loading...' })
    await act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'member', role: 1 })
    )
    expect(requestSignal?.aborted).toBe(true)
    await act(() =>
      complete({
        data: { success: true, data: { trace_id: 'trace-1', legs: [reply] } },
      })
    )
    await waitFor(() =>
      expect(queryClients[0].getQueryCache().getAll()).toHaveLength(0)
    )
    expect(screen.queryByText('Final client reply')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'View request trace' })
    ).not.toBeInTheDocument()
  })

  test('does not offer or fetch a trace when its parent is closed', () => {
    renderSection(false)
    expect(getRequestTrace).not.toHaveBeenCalled()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
