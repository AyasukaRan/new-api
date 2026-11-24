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
  flexRender,
  getCoreRowModel,
  useReactTable,
  type VisibilityState,
} from '@tanstack/react-table'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, test, vi } from 'vitest'

import { DataTableViewOptions } from '@/components/data-table'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema } from '../../data/schema'
import type { LogOtherData, RequestTraceLeg } from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { DetailsDialog } from '../dialogs/details-dialog'
import { RequestTraceSection } from '../dialogs/request-trace-section'
import { RequestMetadataTags } from '../request-metadata-tags'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'
import { UsageLogsProvider } from '../usage-logs-provider'

const metadata: LogOtherData = {
  reasoning_effort: 'high',
  request_path: '/v1/responses',
  response_transport: 'sse',
  user_agent: 'codex-tui/1.0',
  client_tool: 'Codex CLI',
  invoked_tools: ['exec_command', 'apply_patch', 'read_resource'],
  invoked_tool_counts: { exec_command: 3 },
  admin_info: { trace_id: 'private-trace' },
  tool_observation: 'complete',
}
const log = usageLogSchema.parse({
  id: 1,
  user_id: 1,
  created_at: 1,
  type: 2,
  content: '',
  model_name: 'deepseek-v4-pro',
  username: 'account-user',
  token_name: 'token-1',
  group: 'DeepSeek',
  ip: '192.0.2.25',
  is_stream: true,
  use_time: 4,
  completion_tokens: 100,
  other: JSON.stringify({ ...metadata, frt: 450 }),
})
const clients: QueryClient[] = []

const onShowDetails = () => undefined

function renderWithQueries(element: ReactNode, client?: QueryClient) {
  const queryClient =
    client ?? new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(queryClient)
  const updatedAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt })
  queryClient.setQueryData(
    ['pricing'],
    { data: [], vendors: [] },
    { updatedAt }
  )
  return render(
    <QueryClientProvider client={queryClient}>
      <UsageLogsProvider>{element}</UsageLogsProvider>
    </QueryClientProvider>
  )
}

function LogSurface(props: {
  mobile: boolean
  isAdmin: boolean
  compact?: boolean
  visibility?: VisibilityState
  showViewMenu?: boolean
}) {
  const columns = useCommonLogsColumns(
    props.isAdmin,
    onShowDetails,
    'usage',
    false,
    props.compact
  )
  const table = useReactTable({
    data: [log],
    columns,
    state: { columnVisibility: props.visibility ?? {} },
    getCoreRowModel: getCoreRowModel(),
  })
  if (props.showViewMenu) return <DataTableViewOptions table={table} />
  if (props.mobile) {
    return <UsageLogsMobileList table={table} logCategory='common' />
  }
  return (
    <table>
      <tbody>
        <tr>
          {table
            .getRowModel()
            .rows[0].getVisibleCells()
            .map((cell) => (
              <td key={cell.id} aria-label={cell.column.id}>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
        </tr>
      </tbody>
    </table>
  )
}

afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

test('leaves old records without metadata unlabelled instead of claiming no tools were called', () => {
  const view = render(<RequestMetadataTags isAdmin metadata={{}} expanded />)
  expect(
    screen.queryByRole('group', { name: 'Request attributes' })
  ).not.toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags
      isAdmin
      metadata={{ tool_observation: 'complete' }}
      expanded
    />
  )
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags
      isAdmin
      metadata={{ reasoning_effort: 'none' }}
      expanded
    />
  )
  expect(screen.getByText('Reasoning: none')).toBeInTheDocument()
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
})

test('distinguishes confirmed empty tool output from a partial observation', () => {
  const view = render(
    <RequestMetadataTags
      isAdmin
      metadata={{ invoked_tools: [], tool_observation: 'complete' }}
      expanded
    />
  )
  expect(screen.getByText('No tools invoked')).toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags
      isAdmin
      metadata={{ invoked_tools: [], tool_observation: 'partial' }}
      expanded
    />
  )
  expect(screen.getByText('Tool details incomplete')).toBeInTheDocument()
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
})

test('shows two tools and lets keyboard users expand the remaining tool names', async () => {
  const user = userEvent.setup()
  render(<RequestMetadataTags isAdmin metadata={metadata} />)
  expect(screen.getByText('Reasoning: high')).toBeInTheDocument()
  expect(screen.getByTitle('Client: Codex CLI')).toHaveTextContent('Codex CLI')
  expect(screen.queryByText('Tool: read_resource')).not.toBeInTheDocument()
  const more = screen.getByRole('button', { name: 'Show all invoked tools' })
  expect(more).toHaveTextContent('+1')
  expect(more.closest('[data-slot=request-tool-tags]')).toHaveClass(
    'w-full',
    '[&>div]:flex-wrap',
    '[&>div>div]:flex-wrap'
  )
  expect(
    screen.getByText('Tool: exec_command').closest('[data-slot=status-badge]')
  ).toHaveClass('h-auto', 'min-h-5', 'shrink-0')
  more.focus()
  await user.keyboard('{Enter}')
  expect(await screen.findByText('Tool: read_resource')).toHaveClass(
    'whitespace-normal',
    'break-all'
  )
  expect(more).toHaveAttribute('aria-expanded', 'true')
  expect(
    screen.getByText('Tool: read_resource').closest('[data-slot=status-badge]')
  ).toHaveClass('h-auto', 'min-h-5')
  await user.keyboard('{Escape}')
  await waitFor(() => expect(more).toHaveAttribute('aria-expanded', 'false'))
  expect(more).toHaveFocus()
})

test('wraps complete long tool names in details and ignores malformed or duplicate names', async () => {
  const longName =
    'mcp__company_support__lookup_customer_account_and_active_subscriptions'
  const malformed = {
    client_tool: 123,
    reasoning_effort: {},
    invoked_tools: [
      null,
      '',
      '  ',
      longName,
      longName,
      '<img src=x onerror=alert(1)>',
    ],
  } as unknown as LogOtherData
  const i18n = i18next.createInstance()
  await i18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
    // Match the app: React escapes text after i18next interpolation.
    interpolation: { escapeValue: false },
  })
  const view = render(
    <I18nextProvider i18n={i18n}>
      <RequestMetadataTags isAdmin metadata={malformed} expanded />
    </I18nextProvider>
  )
  const group = screen.getByRole('group', { name: 'Request attributes' })
  expect(group).toHaveClass('flex-wrap', 'min-w-0')
  expect(screen.getAllByText(`Tool: ${longName}`)).toHaveLength(1)
  expect(screen.getByText(`Tool: ${longName}`)).toHaveClass(
    'whitespace-normal',
    'break-all'
  )
  expect(
    screen.getByText(`Tool: ${longName}`).closest('[data-slot=status-badge]')
  ).toHaveClass('h-auto', 'min-h-5')
  expect(
    screen.getByText('Tool: <img src=x onerror=alert(1)>')
  ).toBeInTheDocument()
  expect(view.container.querySelector('img')).toBeNull()
  expect(screen.queryByText(/Reasoning:/)).not.toBeInTheDocument()
})

test('groups the account, request source, transport and token metrics into distinct cells', () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  renderWithQueries(<LogSurface mobile={false} isAdmin />)
  const cell = (name: string) => screen.getByRole('cell', { name })
  expect(cell('model_name')).toHaveTextContent(log.model_name)
  expect(cell('model_name')).not.toHaveTextContent(
    /Codex CLI|high|exec_command/
  )
  expect(cell('reasoning_effort')).toHaveTextContent('high')
  const account = within(cell('token_name'))
  for (const text of [log.username, log.token_name, log.group]) {
    expect(account.getByText(text)).toBeVisible()
  }
  const source = within(cell('request_source'))
  for (const text of ['Codex CLI', 'codex-tui/1.0', log.ip]) {
    expect(source.getByText(text)).toBeVisible()
  }
  const tools = within(cell('invoked_tools'))
  for (const name of ['exec_command', 'apply_patch', 'read_resource']) {
    expect(tools.getByText(name)).toHaveClass('sr-only')
  }
  expect(
    tools.queryByRole('button', { name: 'Show all invoked tools' })
  ).not.toBeInTheDocument()
  const transport = within(cell('is_stream'))
  expect(transport.getByText('/v1/responses')).toBeVisible()
  expect(transport.getByText('SSE')).toBeVisible()
  const performance = within(cell('prompt_tokens'))
  expect(performance.getByRole('term', { name: 'Output Tokens' })).toBeVisible()
  for (const text of ['450ms', '4.0s', '25 t/s']) {
    expect(performance.getByText(text)).toBeVisible()
  }
  expect(cell('is_stream')).not.toHaveTextContent('t/s')
})

test('opens a fourth tool with the keyboard when using compact tool icons', async () => {
  const user = userEvent.setup()
  render(
    <RequestMetadataTags
      field='invoked_tools'
      isAdmin
      metadata={{
        invoked_tools: ['bash', 'read', 'web_search', 'file_search'],
        invoked_tool_counts: { bash: 4, file_search: 0.5 },
        web_search_call_count: 3,
        file_search_call_count: 7,
      }}
    />
  )
  expect(screen.getByText('web_search')).toHaveClass('sr-only')
  expect(screen.queryByText('file_search')).not.toBeInTheDocument()
  const more = screen.getByRole('button', { name: 'Show all invoked tools' })
  more.focus()
  await user.keyboard('{Enter}')
  const panel = within(await screen.findByRole('dialog'))
  const counts = panel.getAllByRole('definition').map((row) => row.textContent)
  expect(counts).toEqual([
    'Count ×4',
    'Count Not recorded',
    'Count ×3',
    'Count ×7',
  ])
  expect(panel.getAllByRole('term').map((term) => term.textContent)).toEqual([
    'bash',
    'read',
    'web_search',
    'file_search',
  ])
  await user.keyboard('{Escape}')
  await waitFor(() => expect(more).toHaveAttribute('aria-expanded', 'false'))
  expect(more).toHaveFocus()
})

test.each(['desktop', 'compact', 'mobile'] as const)(
  'shows only the selected tool type and count without loading its trace (%s)',
  async (surface) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    const get = vi.spyOn(api, 'get')
    renderWithQueries(
      <LogSurface
        mobile={surface === 'mobile'}
        compact={surface === 'compact'}
        isAdmin
      />
    )
    const area =
      surface === 'compact'
        ? within(screen.getByRole('cell', { name: 'model_name' }))
        : screen
    const target = area.getByRole('button', { name: 'Tool: exec_command' })
    if (surface === 'mobile') await userEvent.click(target)
    else await userEvent.hover(target)
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getAllByRole('term')).toHaveLength(1)
    expect(dialog.getByRole('term')).toHaveTextContent('exec_command')
    expect(dialog.getByRole('definition')).toHaveTextContent('×3')
    expect(dialog.queryByText('Parameters')).not.toBeInTheDocument()
    expect(dialog.queryByText('Tool result')).not.toBeInTheDocument()
    expect(
      get.mock.calls.some(([path]) => String(path).startsWith('/api/log/trace'))
    ).toBe(false)
  }
)

test.each([false, true])(
  'allows hiding model and client independently of reasoning and tools (mobile: %s)',
  (mobile) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    renderWithQueries(
      <LogSurface
        mobile={mobile}
        isAdmin
        visibility={{
          model_name: false,
          client_tool: false,
          request_path: false,
        }}
      />
    )
    expect(screen.queryByText(log.model_name)).not.toBeInTheDocument()
    expect(screen.queryByText('Codex CLI')).not.toBeInTheDocument()
    expect(screen.queryByText('/v1/responses')).not.toBeInTheDocument()
    expect(screen.getByText('codex-tui/1.0')).toBeVisible()
    expect(screen.getByText(log.ip)).toBeVisible()
    expect(screen.getByText('SSE')).toBeVisible()
    expect(screen.getByText(/^(Reasoning: )?high$/)).toBeVisible()
    expect(screen.getByText(/^(Tool: )?exec_command$/)).toBeVisible()
  }
)

test.each([false, true])(
  'allows hiding the IP and response mode while keeping the other details (mobile: %s)',
  (mobile) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    renderWithQueries(
      <LogSurface
        mobile={mobile}
        isAdmin
        visibility={{ ip: false, response_transport: false }}
      />
    )
    expect(screen.queryByText(log.ip)).not.toBeInTheDocument()
    expect(screen.queryByText('SSE')).not.toBeInTheDocument()
    expect(screen.getByText('Codex CLI')).toBeVisible()
    expect(screen.getByText('codex-tui/1.0')).toBeVisible()
    expect(screen.getByText('/v1/responses')).toBeVisible()
  }
)

test.each([false, true])(
  'hides admin metadata from personal logs even if the row contains it (mobile: %s)',
  (mobile) => {
    renderWithQueries(<LogSurface mobile={mobile} isAdmin={false} />)
    expect(screen.getByText(/^(Reasoning: )?high$/)).toBeVisible()
    expect(screen.queryByText('Codex CLI')).not.toBeInTheDocument()
    expect(screen.queryByText(/exec_command/)).not.toBeInTheDocument()
    expect(screen.queryByText('codex-tui/1.0')).not.toBeInTheDocument()
    expect(screen.queryByText(log.username)).not.toBeInTheDocument()
    expect(screen.getByText(log.ip)).toBeVisible()
    expect(screen.getByText(log.token_name)).toBeVisible()
    expect(screen.getByText('SSE')).toBeVisible()
    expect(screen.queryByText('/v1/responses')).not.toBeInTheDocument()
  }
)

test('updates the column picker when an admin switches from all logs to personal logs', async () => {
  const user = userEvent.setup()
  function SwitchScope() {
    const [isAdmin, setIsAdmin] = useState(true)
    return (
      <>
        <button type='button' onClick={() => setIsAdmin(false)}>
          Personal logs
        </button>
        <LogSurface mobile={false} isAdmin={isAdmin} showViewMenu />
      </>
    )
  }
  renderWithQueries(<SwitchScope />)
  await user.click(screen.getByRole('button', { name: 'View' }))
  expect(screen.getByRole('menuitemcheckbox', { name: 'Tools' })).toBeVisible()
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Personal logs' }))
  await user.click(screen.getByRole('button', { name: 'View' }))
  expect(
    screen.getByRole('menuitemcheckbox', { name: 'Reasoning Effort' })
  ).toBeVisible()
  expect(
    screen.queryByRole('menuitemcheckbox', { name: 'Tools' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('menuitemcheckbox', { name: 'Client' })
  ).not.toBeInTheDocument()
})

test('shows only reasoning in owner details even when cached data contains admin tags', () => {
  renderWithQueries(
    <DetailsDialog
      log={log}
      isAdmin={false}
      isRoot={false}
      open
      onOpenChange={() => undefined}
    />
  )
  expect(screen.getByText('Reasoning Effort')).toBeVisible()
  expect(screen.getByText('high')).toBeVisible()
  expect(screen.queryByText('Codex CLI')).not.toBeInTheDocument()
  expect(screen.queryByText(/Tool:/)).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Show all invoked tools' })
  ).not.toBeInTheDocument()
})

test('keeps request tags visible when the administrator opens the trace', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const leg: RequestTraceLeg = {
    seq: 0,
    attempt: 0,
    direction: 'client_response',
    channel_id: 0,
    format: 'openai',
    method: '',
    url: '',
    status: 200,
    headers: null,
    body: '',
    body_size: 0,
    truncated: false,
    has_object: false,
    rendered: { content: 'Hello', stream: false },
  }
  client.setQueryData(
    ['usage-logs', 'request-trace', 1, 10, 'trace-tags'],
    [leg]
  )
  renderWithQueries(
    <RequestTraceSection traceId='trace-tags' enabled metadata={metadata} />,
    client
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'View request trace' })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Request Trace' })
  await userEvent.click(
    await within(dialog).findByRole('button', {
      name: 'Show all invoked tools',
    })
  )
  expect(await screen.findByText('Tool: read_resource')).toBeInTheDocument()
  expect(within(dialog).getByText('Reasoning: high')).toBeInTheDocument()
  expect(within(dialog).getByText('Codex CLI')).toBeInTheDocument()
})

test.each([
  ['enabled', 'enabled'],
  ['adaptive', 'adaptive'],
  ['low', 'low'],
  ['medium', 'medium'],
  ['xhigh', 'xhigh'],
  ['max', 'max'],
  ['  Provider_Custom_Level  ', 'Provider_Custom_Level'],
])(
  'preserves provider effort %s without translation or case normalization',
  (effort, label) => {
    render(
      <RequestMetadataTags isAdmin metadata={{ reasoning_effort: effort }} />
    )
    expect(screen.getByText(`Reasoning: ${label}`)).toBeInTheDocument()
  }
)
