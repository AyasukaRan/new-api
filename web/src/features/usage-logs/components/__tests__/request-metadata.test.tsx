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
} from '@tanstack/react-table'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import type { ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, test } from 'vitest'

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
  client_tool: 'Codex CLI',
  invoked_tools: ['exec_command', 'apply_patch', 'read_resource'],
  tool_observation: 'complete',
}
const log = usageLogSchema.parse({
  id: 1,
  user_id: 1,
  created_at: 1,
  type: 2,
  content: '',
  model_name: 'deepseek-v4-pro',
  other: JSON.stringify(metadata),
})
const clients: QueryClient[] = []

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

function LogSurface(props: { mobile: boolean }) {
  const columns = useCommonLogsColumns(false, false)
  const table = useReactTable({
    data: [log],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  if (props.mobile) {
    return <UsageLogsMobileList table={table} logCategory='common' />
  }
  const cell = table
    .getRowModel()
    .rows[0].getAllCells()
    .find((item) => item.column.id === 'model_name')
  if (!cell) {
    throw new Error('Model column is missing')
  }
  return <>{flexRender(cell.column.columnDef.cell, cell.getContext())}</>
}

afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  useAuthStore.getState().auth.reset()
})

test('leaves old records without metadata unlabelled instead of claiming no tools were called', () => {
  const view = render(<RequestMetadataTags metadata={{}} expanded />)
  expect(
    screen.queryByRole('group', { name: 'Request attributes' })
  ).not.toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags metadata={{ tool_observation: 'complete' }} expanded />
  )
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags metadata={{ reasoning_effort: 'none' }} expanded />
  )
  expect(screen.getByText('Reasoning: none')).toBeInTheDocument()
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
})

test('distinguishes confirmed empty tool output from a partial observation', () => {
  const view = render(
    <RequestMetadataTags
      metadata={{ invoked_tools: [], tool_observation: 'complete' }}
      expanded
    />
  )
  expect(screen.getByText('No tools invoked')).toBeInTheDocument()
  view.rerender(
    <RequestMetadataTags
      metadata={{ invoked_tools: [], tool_observation: 'partial' }}
      expanded
    />
  )
  expect(screen.getByText('Tool details incomplete')).toBeInTheDocument()
  expect(screen.queryByText('No tools invoked')).not.toBeInTheDocument()
})

test('shows two tools and lets keyboard users expand the remaining tool names', async () => {
  const user = userEvent.setup()
  render(<RequestMetadataTags metadata={metadata} />)
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
      <RequestMetadataTags metadata={malformed} expanded />
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

test.each([false, true])(
  'shows the same request tags in the model summary when mobile is %s',
  (mobile) => {
    renderWithQueries(<LogSurface mobile={mobile} />)
    const group = screen.getByRole('group', { name: 'Request attributes' })
    expect(within(group).getByText('Reasoning: high')).toBeInTheDocument()
    expect(within(group).getByText('Codex CLI')).toBeInTheDocument()
    expect(within(group).getByText('Tool: exec_command')).toBeInTheDocument()
    expect(
      within(group).getByRole('button', { name: 'Show all invoked tools' })
    ).toBeInTheDocument()
  }
)

test('shows all request tags in owner details without a duplicate reasoning row', () => {
  renderWithQueries(
    <DetailsDialog
      log={log}
      isAdmin={false}
      isRoot={false}
      open
      onOpenChange={() => undefined}
    />
  )
  expect(screen.getAllByText('Reasoning: high')).toHaveLength(1)
  expect(screen.queryByText('Reasoning Effort')).not.toBeInTheDocument()
  expect(screen.getByText('Tool: read_resource')).toBeInTheDocument()
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
    render(<RequestMetadataTags metadata={{ reasoning_effort: effort }} />)
    expect(screen.getByText(`Reasoning: ${label}`)).toBeInTheDocument()
  }
)
