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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { UsageLogs } from '../..'
import { usageLogSchema } from '../../data/schema'

function entry(id: number) {
  return usageLogSchema.parse({
    id,
    live_key: `key-${id}`,
    user_id: 1,
    created_at: 1788840000 + Math.floor(id / 10),
    type: 2,
    content: `Summary for request ${id}`,
    model_name: `model-${id}`,
    request_id: `request-${id}`,
    other: JSON.stringify({
      client_tool: 'Codex CLI',
      admin_info: { trace_id: `trace-${id}` },
    }),
  })
}

afterEach(() => {
  cleanup()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  localStorage.clear()
})

function renderLogs(
  live = false,
  path = '/usage-logs/common',
  content?: string | ((id: number) => string),
  initialIds = [11, 10]
) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'tester', role: ROLE.ADMIN })
  let records = initialIds.map(entry).map((record) => ({
    ...record,
    content:
      typeof content === 'function'
        ? content(record.id)
        : (content ?? record.content),
  }))
  let liveTotal = 0
  const selfRecords = [entry(100)]
  const initialTotal = path.includes('page=2') ? 205 : 2
  const requestedTraces: string[] = []
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data: unknown = {}
    if (url.startsWith('/api/log/?')) {
      data = {
        items: records,
        total: initialTotal,
        ...(live && { live_cursor: 'initial' }),
      }
    } else if (url.startsWith('/api/log/self?')) {
      data = { items: selfRecords, total: 1, live_cursor: 'self-initial' }
    } else if (url === '/api/log/stream' || url === '/api/log/self/stream') {
      return {
        data: new ReadableStream({
          start(controller) {
            stream = controller
          },
        }),
      }
    } else if (
      url.startsWith('/api/log/stat?') &&
      url.includes('live_total=1')
    ) {
      data = { live_total: liveTotal, quota: 0, rpm: 0, tpm: 0 }
    } else if (url === '/api/log/trace') {
      const params = config?.params as { trace_id?: string } | undefined
      const trace = params?.trace_id
      if (!trace) throw new Error('Expected a trace ID')
      requestedTraces.push(trace)
      data = {
        trace_id: trace,
        legs: [
          {
            seq: 1,
            attempt: 0,
            direction: 'client_response',
            channel_id: 1,
            format: 'openai',
            method: 'POST',
            url: '',
            status: 200,
            headers: null,
            body: '',
            body_size: 0,
            truncated: false,
            has_object: false,
            rendered: { content: `Reply for ${trace}`, stream: false },
          },
        ],
      }
    } else if (url === '/api/user/') {
      data = { items: [{ id: 12, username: 'recent-user' }], total: 1 }
    } else if (url === '/api/group/') {
      data = ['premium']
    } else if (url === '/api/subscription/admin/plans') {
      data = []
    }
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: UsageLogs,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const batch = async (ids: number[], omitted = 0) => {
    await waitFor(() => expect(stream).toBeDefined())
    const wire = new TextEncoder().encode(
      `event: logs\r\ndata: ${JSON.stringify({ items: ids.map(entry), omitted, cursor: String(ids[0]) })}\r\n\r\n`
    )
    await act(async () => {
      stream?.enqueue(wire.slice(0, 12))
      stream?.enqueue(wire.slice(12))
    })
  }
  return {
    router,
    requestedTraces,
    reset: async (ids: number[]) => {
      records = ids.map(entry)
      await act(async () => {
        stream?.enqueue(new TextEncoder().encode('event: reset\ndata: {}\n\n'))
      })
    },
    batch,
    append: (...ids: number[]) => batch(ids),
    refreshStats: async (total: number) => {
      liveTotal = total
      await act(async () => {
        await client.invalidateQueries({ queryKey: ['usage-logs-stats'] })
      })
    },
    refresh: async (ids: number[]) => {
      records = ids.map(entry)
      await act(async () => {
        await client.invalidateQueries({ queryKey: ['logs'] })
      })
      await waitFor(() =>
        expect(screen.getByText(records[0].model_name)).toBeInTheDocument()
      )
    },
  }
}

test('column filters apply across pages and clear only their own fields', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(false, '/usage-logs/common?page=2&username=fp')
  const modelFilter = await screen.findByRole('button', {
    name: 'Filter · Model',
  })
  await user.click(modelFilter)
  const model = await screen.findByRole('combobox', { name: 'Model Name' })
  await user.type(model, 'off-page-model')
  expect(router.state.location.search).toMatchObject({
    page: 2,
    username: 'fp',
  })
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      page: 1,
      username: 'fp',
      model: 'off-page-model',
    })
  )
  await waitFor(() =>
    expect(
      vi.mocked(api.get).mock.calls.some(([path]) => {
        const url = new URL(path, 'http://localhost')
        return (
          url.pathname === '/api/log/' &&
          url.searchParams.get('model_name') === 'off-page-model'
        )
      })
    ).toBe(true)
  )
  await user.click(screen.getByRole('button', { name: 'Filter · Model (1)' }))
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  await waitFor(() =>
    expect(router.state.location.search.model).toBeUndefined()
  )
  expect(router.state.location.search.username).toBe('fp')

  await user.click(screen.getByRole('button', { name: 'Filter · Channel' }))
  const channel = screen.getByLabelText('Channel ID')
  await user.type(channel, '-1{Enter}')
  expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Enter a positive integer'
  )
  expect(router.state.location.search.channel).toBeUndefined()
})

test('filters error logs from the Time header and preserves the other filters when cleared', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?page=2&group=default&type=%5B%220%22%5D'
  )
  const button = await screen.findByRole('button', { name: 'Filter · Time' })
  expect(
    screen.queryByRole('combobox', { name: 'Type' })
  ).not.toBeInTheDocument()
  button.focus()
  await user.keyboard('{Enter}')
  await user.click(screen.getByRole('combobox', { name: 'Type' }))
  await user.click(await screen.findByRole('option', { name: 'Error' }))
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      type: ['5'],
      group: 'default',
      page: 1,
    })
  )
  await user.click(screen.getByRole('button', { name: 'Filter · Time (1)' }))
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Filter · Time' })).toBeVisible()
  )
  expect(router.state.location.search.type).toBeUndefined()
  expect(router.state.location.search.group).toBe('default')
})

test('can select a protocol and enter an off-page endpoint from an empty log table', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?page=2&type=%5B%225%22%5D',
    undefined,
    []
  )
  await user.click(
    await screen.findByRole('button', { name: 'Filter · Transport' })
  )
  const panel = screen.getByRole('dialog', { name: 'Transport' })
  await user.click(
    within(panel).getByRole('combobox', { name: 'Response mode' })
  )
  await user.click(await screen.findByRole('option', { name: 'SSE' }))
  await user.type(within(panel).getByLabelText('Endpoint'), '/v1/responses')
  await user.click(within(panel).getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      type: ['5'],
      responseTransport: 'sse',
      requestPath: '/v1/responses',
      page: 1,
    })
  )
  await waitFor(() =>
    expect(
      vi.mocked(api.get).mock.calls.some(([path]) => {
        const url = new URL(path, 'http://localhost')
        return (
          url.pathname === '/api/log/' &&
          url.searchParams.get('response_transport') === 'sse' &&
          url.searchParams.get('request_path') === '/v1/responses'
        )
      })
    ).toBe(true)
  )
  await user.click(
    screen.getByRole('button', { name: 'Filter · Transport (2)' })
  )
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  await waitFor(() =>
    expect(router.state.location.search.responseTransport).toBeUndefined()
  )
  expect(router.state.location.search.requestPath).toBeUndefined()
  expect(router.state.location.search.type).toEqual(['5'])
})

test('keeps transport filters editable in the toolbar when the column is hidden', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?responseTransport=sse&requestPath=%2Fv1%2Fmessages'
  )
  await screen.findByRole('button', { name: 'Filter · Transport (2)' })
  await user.click(screen.getByRole('button', { name: 'Column settings' }))
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'Transport' }))
  await user.keyboard('{Escape}')
  expect(
    screen.queryByRole('button', { name: /Filter · Transport/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Expand/ }))
  const mode = screen.getByRole('combobox', { name: 'Response mode' })
  expect(mode).toHaveTextContent('SSE')
  expect(screen.getByLabelText('Endpoint')).toHaveValue('/v1/messages')
  await user.click(mode)
  await user.click(await screen.findByRole('option', { name: 'HTTP' }))
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      responseTransport: 'http',
      requestPath: '/v1/messages',
    })
  )
})

test('opens the identity filter without obscuring its fields and selects a username inline', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs()
  await user.click(
    await screen.findByRole('button', { name: 'Filter · User / Token' })
  )
  const panel = await screen.findByRole('dialog', { name: 'User / Token' })
  const fields = within(panel)
  const username = fields.getByRole('combobox', { name: 'Username' })
  const token = fields.getByRole('textbox', { name: 'Token Name' })
  const group = fields.getByRole('combobox', { name: 'Group' })
  await waitFor(() => expect(username).toHaveFocus())
  expect(username).toHaveAttribute('aria-expanded', 'false')
  expect(fields.queryByRole('listbox')).not.toBeInTheDocument()
  expect(fields.getByRole('button', { name: 'Apply' })).toBeEnabled()
  await user.tab()
  expect(token).toHaveFocus()
  await user.keyboard('work')
  await user.tab()
  expect(group).toHaveFocus()
  expect(group).toHaveAttribute('aria-expanded', 'false')
  await user.click(group)
  await user.click(await fields.findByRole('option', { name: 'premium' }))
  await user.click(username)
  expect(
    await fields.findByRole('option', { name: 'recent-user' })
  ).toBeVisible()
  const suggestions = fields.getByRole('listbox')
  expect(username.closest('form')).toContainElement(suggestions)
  expect(
    suggestions.compareDocumentPosition(token) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  await user.keyboard('{Escape}')
  expect(username).toHaveValue('')
  await user.keyboard('{ArrowDown}')
  await fields.findByRole('option', { name: 'recent-user' })
  await user.keyboard('{ArrowDown}{Enter}')
  expect(username).toHaveValue('recent-user')
  expect(username).toHaveAttribute('aria-expanded', 'false')
  expect(router.state.location.search.username).toBeUndefined()
  await user.click(fields.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      username: 'recent-user',
      token: 'work',
      group: 'premium',
    })
  )
})

test('keeps client suggestions within the source form when clearing both fields', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?model=model-11&clientTool=Codex+CLI&ip=192.0.2.10'
  )
  await user.click(
    await screen.findByRole('button', { name: 'Filter · Request source (2)' })
  )
  const panel = await screen.findByRole('dialog', { name: 'Request source' })
  const clientInput = within(panel).getByRole('combobox', { name: 'Client' })
  await user.click(clientInput)
  const suggestions = await within(panel).findByRole('listbox')
  expect(clientInput.closest('form')).toContainElement(suggestions)
  const clear = within(panel).getByRole('button', { name: 'Clear' })
  await user.pointer({ target: clear, keys: '[MouseLeft>]' })
  expect(suggestions).toBeVisible()
  await user.pointer({ target: clear, keys: '[/MouseLeft]' })
  await waitFor(() =>
    expect(router.state.location.search.clientTool).toBeUndefined()
  )
  expect(router.state.location.search.ip).toBeUndefined()
  expect(router.state.location.search.model).toBe('model-11')
})

test.each(['desktop', 'mobile'] as const)(
  'keeps the selected %s trace open as refreshed logs move it off the current page',
  async (layout) => {
    if (layout === 'mobile') {
      const previous = window.matchMedia
      vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
        ...previous(query),
        matches: query === '(max-width: 640px)' || previous(query).matches,
      }))
    }
    const user = userEvent.setup()
    const { refresh, requestedTraces } = renderLogs()
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 11' })
    )
    await user.click(
      await screen.findByRole('button', { name: 'View request trace' })
    )
    const trace = await screen.findByRole('dialog', { name: 'Request Trace' })
    expect(await within(trace).findByText('Reply for trace-11')).toBeVisible()
    await user.click(
      within(trace).getByRole('tab', { name: 'Exchange details' })
    )

    for (const ids of [
      [12, 11],
      [13, 12],
    ]) {
      await refresh(ids)
      expect(requestedTraces).toEqual(['trace-11'])
      const openTrace = within(
        screen.getByRole('dialog', { name: 'Request Trace' })
      )
      expect(openTrace.getByText('trace-11')).toBeVisible()
      expect(
        openTrace.getByRole('tab', { name: 'Exchange details' })
      ).toHaveAttribute('aria-selected', 'true')
    }

    await user.keyboard('{Escape}')
    const details = await screen.findByRole('dialog', { name: /Log Details/ })
    await user.click(within(details).getByRole('tab', { name: 'Overview' }))
    expect(within(details).getByText('request-11')).toBeVisible()
    await user.keyboard('{Escape}')
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 13' })
    )
    await user.click(
      await screen.findByRole('button', { name: 'View request trace' })
    )
    expect(await screen.findByText('Reply for trace-13')).toBeVisible()
  }
)

test('clears the selected trace and details when the signed-in account changes', async () => {
  const user = userEvent.setup()
  renderLogs()
  await user.click(
    await screen.findByRole('button', { name: 'Summary for request 11' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'View request trace' })
  )
  expect(await screen.findByText('Reply for trace-11')).toBeVisible()
  await act(async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'other-admin', role: ROLE.ADMIN })
  })
  await screen.findByRole('button', { name: 'Summary for request 11' })
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(screen.queryByText('Reply for trace-11')).not.toBeInTheDocument()
})

test('follows new logs once, and keeps the table and selected details still until requested', async () => {
  const user = userEvent.setup()
  const { append } = renderLogs(true)
  await screen.findByText('model-11')
  await append(12, 11)
  expect(await screen.findByText('model-12')).toBeVisible()
  expect(screen.getAllByText('model-11')).toHaveLength(1)
  await user.click(
    screen.getByRole('button', { name: 'Summary for request 12' })
  )
  await append(13)
  expect(screen.queryByText('model-13')).not.toBeInTheDocument()
  const details = screen.getByRole('dialog', { name: /Log Details/ })
  await user.click(within(details).getByRole('tab', { name: 'Overview' }))
  expect(within(details).getByText('request-12')).toBeVisible()
  await user.keyboard('{Escape}')
  expect(screen.queryByText('model-13')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show new logs (1)' }))
  expect(await screen.findByText('model-13')).toBeVisible()
})

test('preserves the server order for same-second rows with page-relative display IDs', async () => {
  const { append } = renderLogs(true)
  await screen.findByText('model-11')
  await append(30, 31)
  const first = await screen.findByText('model-30')
  const second = screen.getByText('model-31')
  expect(first.compareDocumentPosition(second)).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  )
})

test('uses the next filtered stats count when truncated backlog overlaps the snapshot', async () => {
  const { batch, refreshStats } = renderLogs(true)
  const scrollArea = (await screen.findByText('model-11')).closest(
    'table'
  )?.parentElement
  if (!scrollArea) throw new Error('Missing scroll container')
  scrollArea.scrollTop = 90
  fireEvent.scroll(scrollArea)
  await batch([12, 11], 80)
  expect(screen.getByRole('button', { name: 'Show latest logs' })).toBeVisible()
  await refreshStats(83)
  expect(
    vi
      .mocked(api.get)
      .mock.calls.some(
        ([url]) =>
          url.startsWith('/api/log/stat?') && url.includes('live_total=1')
      )
  ).toBe(true)
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  expect(scrollArea.scrollTop).toBe(90)
  const footer = screen.getByText('Rows per page').parentElement?.parentElement
  if (!footer) throw new Error('Expected pagination total')
  expect(within(footer).getByText('2')).toBeVisible()
  expect(within(footer).queryByText('83')).not.toBeInTheDocument()
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Show latest logs' }))
  expect(await screen.findByText('model-12')).toBeVisible()
  expect(within(footer).getByText('83')).toBeVisible()
  expect(
    vi
      .mocked(api.get)
      .mock.calls.filter(([url]) => url.startsWith('/api/log/?'))
  ).toHaveLength(1)
})

test('buffers offscreen rows and recovers a stale cursor only when the reader accepts the latest logs', async () => {
  const user = userEvent.setup()
  const { append, reset } = renderLogs(true)
  const scrollArea = (await screen.findByText('model-11')).closest(
    'table'
  )?.parentElement
  if (!scrollArea) throw new Error('Missing table scroll area')
  scrollArea.scrollTop = 160
  fireEvent.scroll(scrollArea)
  await append(12)
  expect(
    screen.getByRole('button', { name: 'Show new logs (1)' })
  ).toBeVisible()
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  await reset([30, 29])
  expect(screen.queryByText('model-30')).not.toBeInTheDocument()
  expect(scrollArea.scrollTop).toBe(160)
  await user.click(screen.getByRole('button', { name: 'Show latest logs' }))
  expect(await screen.findByText('model-30')).toBeVisible()
  expect(scrollArea.scrollTop).toBe(0)
})

test('resumes with the applied filters and cursor then drops the old stream on a scope change', async () => {
  const user = userEvent.setup()
  const { append } = renderLogs(true, '/usage-logs/common?model=model')
  await screen.findByText('model-11')
  await append(20)
  const calls = () =>
    vi.mocked(api.get).mock.calls.filter(([url]) => url.endsWith('/stream'))
  const first = calls()[0][1]
  if (!first) throw new Error('Expected initial stream')
  expect(first.params).toMatchObject({ model_name: 'model', cursor: 'initial' })
  expect(first.params).not.toHaveProperty('end_timestamp')
  const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
  try {
    fireEvent(document, new Event('visibilitychange'))
    expect(first.signal?.aborted).toBe(true)
    hidden.mockReturnValue(false)
    fireEvent(document, new Event('visibilitychange'))
    await waitFor(() =>
      expect(calls()[1]?.[1]?.params).toMatchObject({ cursor: '20' })
    )
    await user.click(screen.getByRole('tab', { name: 'Only Mine' }))
    expect(await screen.findByText('model-100')).toBeVisible()
    expect(screen.queryByText('model-20')).not.toBeInTheDocument()
    expect(calls()[1][1]?.signal?.aborted).toBe(true)
    expect(calls().at(-1)?.[0]).toBe('/api/log/self/stream')
    expect(calls().at(-1)?.[1]?.params).toMatchObject({
      cursor: 'self-initial',
    })
  } finally {
    hidden.mockRestore()
  }
})

test('keeps the historical page still as new logs arrive and never streams a fixed range', async () => {
  const { append, router } = renderLogs(true, '/usage-logs/common?page=2')
  await screen.findByText('model-11')
  await append(12)
  expect(screen.getByRole('button', { name: 'Show latest logs' })).toBeVisible()
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  expect(router.state.location.search.page).toBe(2)
  await act(async () => {
    await router.navigate({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: { startTime: 1_700_000_000_000, endTime: 1_700_086_400_000 },
    })
  })
  expect(
    screen.queryByRole('button', { name: 'Show latest logs' })
  ).not.toBeInTheDocument()
  const calls = vi
    .mocked(api.get)
    .mock.calls.filter(([url]) => url.endsWith('/stream'))
  expect(calls).toHaveLength(1)
  expect(calls[0][1]?.signal?.aborted).toBe(true)
})

test('opens the chosen request from its named details action when there is no summary', async () => {
  const user = userEvent.setup()
  renderLogs(false, '/usage-logs/common', '')
  const button = await screen.findByRole('button', {
    name: 'Details · request-10',
  })
  expect(screen.getByRole('columnheader', { name: 'Details' })).toHaveAttribute(
    'data-column-id',
    'request_details'
  )
  button.focus()
  await user.keyboard('{Enter}')
  const details = await screen.findByRole('dialog', { name: /Log Details/ })
  expect(within(details).getByText('request-10')).toBeVisible()
})

test('shares one details column between normal requests and event summaries', async () => {
  const user = userEvent.setup()
  renderLogs(false, '/usage-logs/common', (id) =>
    id === 11 ? 'Retried upstream' : ''
  )
  const event = await screen.findByRole('button', { name: 'Retried upstream' })
  expect(
    screen.queryByRole('columnheader', { name: 'Event' })
  ).not.toBeInTheDocument()
  const detailCell = screen.getByRole('cell', { name: /Retried upstream/ })
  expect(
    within(detailCell).getByRole('button', { name: 'Details · request-11' })
  ).toBeVisible()
  const plainCell = screen.getByRole('cell', { name: /^Details · request-10$/ })
  expect(within(plainCell).getAllByRole('button')).toHaveLength(1)
  await user.click(event)
  expect(
    screen.getByRole('tab', { name: 'Technical details' })
  ).toHaveAttribute('aria-selected', 'true')
})

test('can hide inline events and still open request details', async () => {
  const user = userEvent.setup()
  renderLogs()
  const event = await screen.findByRole('button', {
    name: 'Summary for request 10',
  })
  await user.click(screen.getByRole('button', { name: 'Column settings' }))
  const toggle = screen.getByRole('menuitemcheckbox', { name: 'Event' })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  await user.keyboard('{Escape}')
  expect(event).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Details · request-10' }))
  expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
})
