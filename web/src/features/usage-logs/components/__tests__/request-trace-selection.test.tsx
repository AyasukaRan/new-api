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
import { usageLogSchema, type UsageLog } from '../../data/schema'

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
  initialIds = [11, 10],
  olderIds?: number[],
  recordOverrides?: Partial<UsageLog>,
  retryRecords?: UsageLog[]
) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'tester', role: ROLE.ADMIN })
  let records = initialIds.map(entry).map((record) => ({
    ...record,
    ...recordOverrides,
    content:
      typeof content === 'function'
        ? content(record.id)
        : (content ?? record.content),
  }))
  let liveTotal = 0
  const selfRecords = [{ ...entry(100), ...recordOverrides }]
  const defaultTotal = path.includes('page=2') ? 205 : 2
  const initialTotal = olderIds
    ? new Set([...initialIds, ...olderIds]).size
    : defaultTotal
  const requestedTraces: string[] = []
  const requestedRetries: unknown[] = []
  let rejectRetryLoad = false
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data: unknown = {}
    if (url.startsWith('/api/log/?')) {
      const page = new URL(url, 'http://localhost').searchParams.get('p')
      data = {
        items: olderIds && page === '2' ? olderIds.map(entry) : records,
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
    } else if (url === '/api/log/retries' || url === '/api/log/self/retries') {
      requestedRetries.push(config?.params)
      if (rejectRetryLoad) {
        rejectRetryLoad = false
        throw new Error('Retry attempt load failed')
      }
      data = retryRecords ?? []
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
    client,
    requestedTraces,
    requestedRetries,
    failNextRetryLoad: () => {
      rejectRetryLoad = true
    },
    active: async (ids: number[]) => {
      await waitFor(() => expect(stream).toBeDefined())
      const items = ids.map((id) => ({
        ...entry(id),
        type: 0,
        in_progress: true,
      }))
      await act(async () => {
        stream?.enqueue(
          new TextEncoder().encode(
            `event: active\ndata: ${JSON.stringify({ items })}\n\n`
          )
        )
      })
    },
    replace: async (ids: number[], total: number, cursor = 'grouped') => {
      await waitFor(() => expect(stream).toBeDefined())
      records = ids.map(entry)
      await act(async () => {
        stream?.enqueue(
          new TextEncoder().encode(
            `event: logs\ndata: ${JSON.stringify({ replace: true, items: records, total, cursor })}\n\n`
          )
        )
      })
    },
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

test('an in-progress row shows Processing without finalized token, cost, or detail actions', async () => {
  const active = {
    ...entry(11),
    type: 0,
    in_progress: true,
    quota: 34567,
    prompt_tokens: 98765,
    completion_tokens: 43210,
  }
  renderLogs(false, '/usage-logs/common', undefined, [11], undefined, active)
  expect(await screen.findByText('Processing...')).toBeVisible()
  expect(screen.getByText('model-11')).toBeVisible()
  expect(screen.getByText('Codex CLI')).toBeVisible()
  expect(
    screen.getByRole('term', { name: 'Total input tokens' }).nextElementSibling
  ).toHaveTextContent('—')
  expect(screen.queryByText('Consume')).not.toBeInTheDocument()
  expect(screen.queryByText('98,765')).not.toBeInTheDocument()
  expect(screen.queryByText('43,210')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', {
      name: /Token Breakdown|Billing Details|Cost breakdown|Details · request-11/,
    })
  ).not.toBeInTheDocument()
})

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
  expect(
    screen.queryByRole('button', { name: 'Apply' })
  ).not.toBeInTheDocument()
  await user.keyboard('{Enter}')
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
  await user.click(screen.getByRole('option', { name: 'All' }))
  await waitFor(() =>
    expect(router.state.location.search.model).toBeUndefined()
  )
  expect(router.state.location.search.username).toBe('fp')

  await user.click(screen.getByRole('button', { name: 'Filter · Channel' }))
  const channel = screen.getByLabelText('Channel ID')
  await user.type(channel, '-1{Enter}')
  expect(
    screen.queryByRole('button', { name: 'Apply' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Enter a positive integer'
  )
  expect(router.state.location.search.channel).toBeUndefined()
})

test('row values combine filters, replace or remove one field, and preserve the selected time and result', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?page=2&pageSize=50&startTime=1788840000000&endTime=1788926400000&result=failure',
    undefined,
    [11, 10],
    undefined,
    {
      username: 'alice',
      user_admin_nickname: 'Operations',
      token_name: 'work-key',
      group: 'premium',
    }
  )
  await user.click(
    (
      await screen.findAllByRole('button', {
        name: 'Filter · Username: Operations · alice',
      })
    )[0]
  )
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      page: 1,
      pageSize: 50,
      startTime: 1788840000000,
      endTime: 1788926400000,
      result: 'failure',
      username: 'alice',
    })
  )
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  const model = await screen.findByRole('button', {
    name: 'Filter · Model Name: model-11',
  })
  model.focus()
  await user.keyboard('{Enter}')
  await user.click(
    (
      await screen.findAllByRole('button', { name: 'Filter · Group: premium' })
    )[0]
  )
  await user.click(
    (
      await screen.findAllByRole('button', {
        name: 'Filter · Token Name: work-key',
      })
    )[0]
  )
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      username: 'alice',
      model: 'model-11',
      group: 'premium',
      token: 'work-key',
      page: 1,
    })
  )
  await waitFor(() =>
    expect(
      vi.mocked(api.get).mock.calls.some(([path]) => {
        const url = new URL(path, 'http://localhost')
        return (
          url.pathname === '/api/log/' &&
          url.searchParams.get('username') === 'alice' &&
          url.searchParams.get('model_name') === 'model-11' &&
          url.searchParams.get('group') === 'premium' &&
          url.searchParams.get('token_name') === 'work-key'
        )
      })
    ).toBe(true)
  )
  await user.click(
    screen.getByRole('button', { name: 'Filter · Model Name: model-10' })
  )
  await waitFor(() =>
    expect(router.state.location.search.model).toBe('model-10')
  )
  await user.click(
    screen.getByRole('button', {
      name: 'Remove filter · Model Name: model-10',
      pressed: true,
    })
  )
  await waitFor(() =>
    expect(router.state.location.search.model).toBeUndefined()
  )
  await user.click(
    within(screen.getByRole('group', { name: 'Filters' })).getByRole('button', {
      name: 'Remove filter · Username: alice',
    })
  )
  await waitFor(() =>
    expect(router.state.location.search.username).toBeUndefined()
  )
  expect(router.state.location.search).toMatchObject({
    token: 'work-key',
    group: 'premium',
    result: 'failure',
    startTime: 1788840000000,
    endTime: 1788926400000,
  })
})

test('row shortcuts respect hidden values and the personal view', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?username=alice',
    undefined,
    [11],
    undefined,
    {
      username: 'alice',
      user_admin_nickname: 'Operations',
      token_name: 'work-key',
      channel: 7,
    }
  )
  await screen.findByRole('button', {
    name: 'Remove filter · Username: Operations · alice',
  })
  await user.click(screen.getByRole('button', { name: 'Hide' }))
  expect(
    screen.queryByRole('button', {
      name: /(?:Filter|Remove filter) · Username:.*alice/,
    })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Filter · Token Name: work-key' })
  ).not.toBeInTheDocument()
  expect(
    within(screen.getByRole('group', { name: 'Filters' })).getByRole('button')
  ).toHaveAccessibleName('Remove filter · Username: ••••')
  expect(router.state.location.search.username).toBe('alice')
  await user.click(screen.getByRole('button', { name: 'Show' }))
  await user.click(screen.getByRole('button', { name: 'Only Mine' }))
  await screen.findByText('model-100')
  expect(
    screen.queryByRole('button', {
      name: /(?:Filter|Remove filter) · Username:/,
    })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Filter · Channel ID:/ })
  ).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('button', { name: 'Filter · Model Name: model-100' })
  )
  await waitFor(() =>
    expect(router.state.location.search.model).toBe('model-100')
  )
})

test('the Time header no longer exposes the replaced log-type filter', async () => {
  renderLogs(false, '/usage-logs/common?group=default&result=failure')
  await screen.findByRole('button', { name: 'Filter · Model' })
  expect(
    screen.queryByRole('button', { name: 'Filter · Time' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('combobox', { name: 'Type' })
  ).not.toBeInTheDocument()
})

test('can select a protocol and enter an off-page endpoint from an empty log table', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs(
    false,
    '/usage-logs/common?page=2&result=failure',
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
      result: 'failure',
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
  expect(router.state.location.search.result).toBe('failure')
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

test('persists independent identity columns and keeps hidden user and token filters editable', async () => {
  const user = userEvent.setup()
  const storageKey = 'usage-logs:common:admin:column-visibility'
  localStorage.setItem(storageKey, JSON.stringify({ token_name: false }))
  const { router } = renderLogs(
    false,
    '/usage-logs/common?username=alice&token=work&group=premium'
  )
  await screen.findByRole('columnheader', { name: /^User(?: |$)/ })
  expect(
    screen.getByRole('columnheader', { name: /^Group(?: |$)/ })
  ).toBeVisible()
  expect(
    screen.queryByRole('columnheader', { name: /^Token(?: Filter|$)/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Column settings' }))
  expect(
    screen.getByRole('menuitemcheckbox', { name: 'User' })
  ).toHaveAttribute('aria-checked', 'true')
  expect(
    screen.getByRole('menuitemcheckbox', { name: 'Token' })
  ).toHaveAttribute('aria-checked', 'false')
  expect(
    screen.getByRole('menuitemcheckbox', { name: 'Group' })
  ).toHaveAttribute('aria-checked', 'true')
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'User' }))
  await user.keyboard('{Escape}')
  expect(
    screen.queryByRole('columnheader', { name: /^User(?: |$)/ })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('columnheader', { name: /^Group(?: |$)/ })
  ).toBeVisible()
  await waitFor(() =>
    expect(JSON.parse(localStorage.getItem(storageKey) || '{}')).toMatchObject({
      username: false,
      token_name: false,
    })
  )
  const expand = screen.queryByRole('button', { name: /^Expand/ })
  if (expand) await user.click(expand)
  expect(screen.getByRole('combobox', { name: 'Username' })).toHaveValue(
    'alice'
  )
  expect(screen.getByRole('textbox', { name: 'Token Name' })).toHaveValue(
    'work'
  )
  expect(
    screen.queryByRole('combobox', { name: 'Group' })
  ).not.toBeInTheDocument()
  expect(router.state.location.search).toMatchObject({
    username: 'alice',
    token: 'work',
    group: 'premium',
  })
})

test('single-condition columns open direct choices and apply without an extra form', async () => {
  const user = userEvent.setup()
  const { router } = renderLogs()
  await user.click(await screen.findByRole('button', { name: 'Filter · User' }))
  let panel = await screen.findByRole('dialog', { name: 'User' })
  const username = within(panel).getByRole('combobox', { name: 'Username' })
  await waitFor(() => expect(username).toHaveFocus())
  expect(
    within(panel).queryByRole('button', { name: 'Apply' })
  ).not.toBeInTheDocument()
  await user.click(
    await within(panel).findByRole('option', { name: 'recent-user' })
  )
  await waitFor(() =>
    expect(router.state.location.search.username).toBe('recent-user')
  )
  expect(screen.queryByRole('dialog', { name: 'User' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Filter · Token' }))
  panel = await screen.findByRole('dialog', { name: 'Token' })
  await user.type(
    within(panel).getByRole('combobox', { name: 'Token Name' }),
    'work{Enter}'
  )
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      username: 'recent-user',
      token: 'work',
    })
  )

  await user.click(screen.getByRole('button', { name: 'Filter · Group' }))
  panel = await screen.findByRole('dialog', { name: 'Group' })
  await user.click(
    await within(panel).findByRole('option', { name: 'premium' })
  )
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      username: 'recent-user',
      token: 'work',
      group: 'premium',
    })
  )

  await user.click(screen.getByRole('button', { name: 'Filter · Token (1)' }))
  panel = await screen.findByRole('dialog', { name: 'Token' })
  await user.click(within(panel).getByRole('option', { name: 'All' }))
  await waitFor(() =>
    expect(router.state.location.search.token).toBeUndefined()
  )
  expect(router.state.location.search).toMatchObject({
    username: 'recent-user',
    group: 'premium',
  })
})

test('shows only the private nickname in admin user cells and falls back to username', async () => {
  const user = userEvent.setup()
  renderLogs(false, '/usage-logs/common', undefined, [11], undefined, {
    username: 'alice',
    user_admin_nickname: 'Operations',
  })
  const account = await screen.findByRole('button', {
    name: 'Filter · Username: Operations · alice',
  })
  expect(account).toHaveTextContent(/^Operations$/)
  expect(within(account).getByText('Operations')).toHaveAttribute(
    'title',
    'alice'
  )
  await user.click(screen.getByRole('button', { name: 'Hide' }))
  expect(screen.queryByText('Operations')).not.toBeInTheDocument()
  expect(screen.queryByTitle('alice')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show' }))
  await user.click(screen.getByRole('button', { name: 'Only Mine' }))
  await screen.findByText('model-100')
  expect(screen.queryByText('Operations')).not.toBeInTheDocument()
  expect(screen.queryByTitle('alice')).not.toBeInTheDocument()
  cleanup()
  renderLogs(false, '/usage-logs/common', undefined, [11], undefined, {
    username: 'alice',
    user_admin_nickname: '',
  })
  expect(
    await screen.findByRole('button', { name: 'Filter · Username: alice' })
  ).toHaveTextContent(/^alice$/)
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
  const selectedModel = await screen.findByText('model-12')
  expect(selectedModel).toBeVisible()
  expect(screen.getAllByText('model-11')).toHaveLength(1)
  await user.click(
    screen.getByRole('button', { name: 'Summary for request 12' })
  )
  await append(13)
  expect(screen.queryByText('model-13')).not.toBeInTheDocument()
  const details = screen.getByRole('dialog', { name: /Log Details/ })
  fireEvent.wheel(details, { deltaY: -120 })
  fireEvent.wheel(selectedModel, { deltaY: -120 })
  await user.click(within(details).getByRole('tab', { name: 'Overview' }))
  expect(within(details).getByText('request-12')).toBeVisible()
  await user.keyboard('{Escape}')
  expect(screen.queryByText('model-13')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show new logs (1)' }))
  expect(await screen.findByText('model-13')).toBeVisible()
})

test('replaces grouped retries and removes recovered requests from a failure filter without double counting replay', async () => {
  const { replace } = renderLogs(true, '/usage-logs/common?result=failure')
  await screen.findByText('model-11')
  await replace([12], 1)
  expect(await screen.findByText('model-12')).toBeVisible()
  expect(screen.queryByText('model-11')).not.toBeInTheDocument()
  expect(screen.queryByText('model-10')).not.toBeInTheDocument()
  expect(
    screen.getByText('Rows per page').parentElement?.parentElement
      ?.firstElementChild
  ).toHaveTextContent(/^1$/)
  await replace([12], 1)
  expect(screen.getAllByText('model-12')).toHaveLength(1)
  await replace([], 0, 'recovered')
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  expect(await screen.findByText('No Logs Found')).toBeVisible()
})

test('buffers grouped retry replacements while details are open and applies them only when the reader returns to latest', async () => {
  const user = userEvent.setup()
  const { replace } = renderLogs(true)
  await user.click(
    await screen.findByRole('button', { name: 'Summary for request 11' })
  )
  const details = screen.getByRole('dialog', { name: /Log Details/ })
  await user.click(within(details).getByRole('tab', { name: 'Overview' }))
  await replace([12], 1)
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  expect(within(details).getByText('request-11')).toBeVisible()
  await user.keyboard('{Escape}')
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show latest logs' }))
  expect(await screen.findByText('model-12')).toBeVisible()
  expect(screen.queryByText('model-11')).not.toBeInTheDocument()
  expect(
    screen.getByText('Rows per page').parentElement?.parentElement
      ?.firstElementChild
  ).toHaveTextContent(/^1$/)
})

test('ignores stats started before a grouped replacement even after a later overlapping update', async () => {
  const { append, replace, client } = renderLogs(true)
  await screen.findByText('model-11')
  await append(11)
  const originalGet = vi.mocked(api.get).getMockImplementation()
  if (!originalGet) throw new Error('Missing log API fixture')
  let releaseStats!: () => void
  const statsGate = new Promise<void>((resolve) => {
    releaseStats = resolve
  })
  let startedStats = false
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url.startsWith('/api/log/stat?') && url.includes('live_total=1')) {
      startedStats = true
      await statsGate
      return {
        data: {
          success: true,
          data: { live_total: 2, quota: 0, rpm: 0, tpm: 0 },
        },
      }
    }
    return originalGet(url, config)
  })
  let pendingStats!: Promise<void>
  await act(async () => {
    pendingStats = client.invalidateQueries({ queryKey: ['usage-logs-stats'] })
  })
  await waitFor(() => expect(startedStats).toBe(true))
  await replace([12], 1)
  await append(12)
  await act(async () => {
    releaseStats()
    await pendingStats
  })
  expect(
    screen.getByText('Rows per page').parentElement?.parentElement
      ?.firstElementChild
  ).toHaveTextContent(/^1$/)
})

test('loads retry attempts only when details open and lets the reader inspect each original request and trace', async () => {
  const user = userEvent.setup()
  const first = {
    ...entry(11),
    type: 5,
    content: 'status_code=429, rate limited',
  }
  const final = entry(12)
  const grouped = {
    ...final,
    retry_count: 1,
    retry_group_request_id: first.request_id,
    retry_started_at: first.created_at,
  }
  const { requestedTraces, requestedRetries } = renderLogs(
    false,
    '/usage-logs/common',
    undefined,
    [12],
    undefined,
    grouped,
    [first, final]
  )
  await screen.findByText('model-12')
  expect(screen.queryByText('model-11')).not.toBeInTheDocument()
  expect(requestedRetries).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: 'Attempts: 2' }))
  const details = screen.getByRole('dialog', { name: /Log Details/ })
  const attempts = await within(details).findByRole('combobox', {
    name: 'Request attempts',
  })
  expect(attempts).toHaveTextContent('Attempt 2')
  expect(within(details).getByText('request-12')).toBeVisible()
  expect(requestedRetries).toEqual([
    {
      request_id: final.request_id,
      start_timestamp: first.created_at,
      end_timestamp: final.created_at,
    },
  ])
  await user.click(attempts)
  await user.click(await screen.findByRole('option', { name: /Attempt 1/ }))
  expect(attempts).toHaveTextContent('Attempt 1')
  expect(within(details).getByText('request-11')).toBeVisible()
  await user.click(
    within(details).getByRole('button', { name: 'View request trace' })
  )
  expect(await screen.findByText('Reply for trace-11')).toBeVisible()
  expect(requestedTraces).toEqual(['trace-11'])
})

test('keeps the final record readable when retry attempts fail to load and supports retrying the lookup', async () => {
  const user = userEvent.setup()
  const final = entry(12)
  const { failNextRetryLoad, requestedRetries } = renderLogs(
    false,
    '/usage-logs/common',
    undefined,
    [12],
    undefined,
    { ...final, retry_count: 1, retry_started_at: final.created_at },
    [entry(11), final]
  )
  await screen.findByText('model-12')
  failNextRetryLoad()
  await user.click(screen.getByRole('button', { name: 'Attempts: 2' }))
  const details = screen.getByRole('dialog', { name: /Log Details/ })
  expect(await within(details).findByText('Failed to load logs')).toBeVisible()
  expect(within(details).getByText('request-12')).toBeVisible()
  await user.click(within(details).getByRole('button', { name: 'Retry' }))
  expect(
    await within(details).findByRole('combobox', { name: 'Request attempts' })
  ).toBeVisible()
  expect(requestedRetries).toHaveLength(2)
  expect(
    within(details).queryByText('Failed to load logs')
  ).not.toBeInTheDocument()
})

test('reveals buffered live logs only when the reader keeps scrolling up at the top', async () => {
  const { append } = renderLogs(true)
  const viewport = (await screen.findByText('model-11')).closest(
    'table'
  )?.parentElement
  if (!viewport) throw new Error('Missing log list viewport')

  viewport.scrollTop = 90
  fireEvent.scroll(viewport)
  await append(12)
  fireEvent.wheel(viewport, { deltaY: -120 })
  viewport.scrollTop = 0
  fireEvent.scroll(viewport)
  expect(
    screen.getByRole('button', { name: 'Show new logs (1)' })
  ).toBeVisible()
  expect(screen.queryByText('model-12')).not.toBeInTheDocument()

  fireEvent.wheel(viewport, { deltaY: -120 })
  expect(await screen.findByText('model-12')).toBeVisible()
  expect(screen.queryByRole('button', { name: 'Show new logs (1)' })).toBeNull()
  expect(viewport.scrollTop).toBe(0)
})

test('refreshes once for a continued upward wheel gesture even without buffered logs', async () => {
  renderLogs(true)
  const viewport = (await screen.findByText('model-11')).closest(
    'table'
  )?.parentElement
  if (!viewport) throw new Error('Missing log list viewport')
  const listRequests = () =>
    vi
      .mocked(api.get)
      .mock.calls.filter(([url]) => url.startsWith('/api/log/?'))

  expect(listRequests()).toHaveLength(1)
  fireEvent.wheel(viewport, { deltaY: -120 })
  fireEvent.wheel(viewport, { deltaY: -100 })
  fireEvent.wheel(viewport, { deltaY: -80 })
  await waitFor(() => expect(listRequests()).toHaveLength(2))
})

test('loading older logs anchors pagination to committed rows while an earlier request remains active', async () => {
  const { active } = renderLogs(
    true,
    '/usage-logs/common?pageSize=2',
    undefined,
    [11, 10],
    [9, 8]
  )
  const first = await screen.findByText('model-11')
  const viewport = first.closest('table')?.parentElement
  if (!viewport) throw new Error('Missing log list viewport')
  Object.defineProperties(viewport, {
    clientHeight: { value: 200 },
    scrollHeight: { value: 600 },
  })
  await active([1])
  expect(await screen.findByText('model-1')).toBeVisible()
  viewport.scrollTop = 400
  fireEvent.scroll(viewport)
  fireEvent.wheel(viewport, { deltaY: 120 })
  expect(await screen.findByText('model-8')).toBeVisible()
  expect(screen.getByText('model-1')).toBeVisible()
  expect(screen.getByText('model-10')).toBeVisible()
  const pages = vi
    .mocked(api.get)
    .mock.calls.filter(([url]) => url.startsWith('/api/log/?'))
    .map(([url]) => new URL(url, 'http://localhost').searchParams)
  expect(pages[1].get('p')).toBe('2')
  expect(pages[1].get('end_timestamp')).toBe(String(entry(11).created_at))
})

test('appends the next filtered page at the bottom, preserving the visible rows and stopping at the end', async () => {
  const { router, append } = renderLogs(
    true,
    '/usage-logs/common?pageSize=3&username=fp',
    undefined,
    [18, 17, 16],
    [16, 15, 14]
  )
  const first = await screen.findByText('model-18')
  const viewport = first.closest('table')?.parentElement
  if (!viewport) throw new Error('Missing log list viewport')
  Object.defineProperties(viewport, {
    clientHeight: { value: 200 },
    scrollHeight: { value: 600 },
  })
  const pages = () =>
    vi
      .mocked(api.get)
      .mock.calls.filter(([url]) => url.startsWith('/api/log/?'))
      .map(([url]) => new URL(url, 'http://localhost').searchParams)

  viewport.scrollTop = 100
  fireEvent.scroll(viewport)
  await append(20)
  expect(pages().map((query) => query.get('p'))).toEqual(['1'])
  viewport.scrollTop = 400
  fireEvent.scroll(viewport)
  fireEvent.wheel(viewport, { deltaY: 120 })

  const last = await screen.findByText('model-14')
  expect(first.compareDocumentPosition(last)).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  )
  expect(screen.getAllByText('model-16')).toHaveLength(1)
  expect(screen.getByText('model-15')).toBeVisible()
  expect(screen.queryByText('model-20')).toBeNull()
  expect(viewport.scrollTop).toBe(400)
  expect(router.state.location.search.page ?? 1).toBe(1)
  expect(pages()[1].get('username')).toBe('fp')
  expect(pages()[1].get('page_size')).toBe('3')
  await act(async () => {
    fireEvent.wheel(viewport, { deltaY: 120 })
    fireEvent.scroll(viewport)
  })
  expect(pages().map((query) => query.get('p'))).toEqual(['1', '2'])

  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: /^Filter · User(?: \(\d+\))?$/ })
  )
  const identityFilter = screen.getByRole('dialog', { name: 'User' })
  await user.click(within(identityFilter).getByRole('option', { name: 'All' }))
  await waitFor(() =>
    expect(router.state.location.search.username).toBeUndefined()
  )
  expect(screen.queryByText('model-14')).not.toBeInTheDocument()
  expect(screen.queryByText('model-15')).not.toBeInTheDocument()
  expect(screen.getByText('model-18')).toBeVisible()
})

test('finds the frozen tail after two pages of same-second inserts displace it', async () => {
  renderLogs(
    true,
    '/usage-logs/common?pageSize=2',
    undefined,
    [13, 12],
    [11, 10]
  )
  const viewport = (await screen.findByText('model-13')).closest(
    'table'
  )?.parentElement
  if (!viewport) throw new Error('Missing log list viewport')
  Object.defineProperties(viewport, {
    clientHeight: { value: 200 },
    scrollHeight: { value: 600 },
  })

  // The log store committed four more rows in the original head's second.
  // Page 2 now contains only inserts. The old tail is the last row of page 3.
  const ids = [19, 18, 17, 16, 13, 12, 11, 10]
  const apiGet = vi.mocked(api.get)
  const initial = apiGet.getMockImplementation()
  if (!initial) throw new Error('Missing log API fixture')
  apiGet.mockImplementation((url, config) => {
    if (!url.startsWith('/api/log/?')) return initial(url, config)
    const params = new URL(url, 'http://localhost').searchParams
    const offset = (Number(params.get('p')) - 1) * 2
    return Promise.resolve({
      data: {
        success: true,
        data: {
          total: ids.length,
          items: ids.slice(offset, offset + 2).map(entry),
        },
      },
    })
  })

  viewport.scrollTop = 400
  fireEvent.scroll(viewport)
  await screen.findByText('model-10')
  expect(
    screen.getAllByText(/^model-\d+$/).map((node) => node.textContent)
  ).toEqual(['model-13', 'model-12', 'model-11', 'model-10'])
  expect(viewport.scrollTop).toBe(400)
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
    await user.click(screen.getByRole('button', { name: 'Only Mine' }))
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
