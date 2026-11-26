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
import { render, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema, type UsageLog } from '../../data/schema'
import type { UsageLogDetailSelection } from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { DetailsDialog } from '../dialogs/details-dialog'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

const longName = 'enterprise-production-failover-2026-without-any-short-alias'
const log = usageLogSchema.parse({
  id: 1,
  user_id: 2,
  created_at: 1788840000,
  type: 2,
  content: '',
  model_name: longName,
  username: 'production-admin',
  channel: 372,
  channel_name: longName,
  token_name: 'backend-production-token',
  group: 'enterprise-production',
  quota: 123456789,
  use_time: 1.5,
  prompt_tokens: 1200,
  completion_tokens: 800,
  other: JSON.stringify({
    cache_tokens: 300,
    cache_creation_tokens_5m: 200,
    model_ratio: 1,
  }),
})

function Fixture(props: {
  admin?: boolean
  desktop?: boolean
  visibility?: VisibilityState
  logs?: UsageLog[]
  loading?: boolean
}) {
  const [selectedLog, setSelectedLog] =
    useState<UsageLogDetailSelection | null>(null)
  const context = useUsageLogsContext()
  const columns = useCommonLogsColumns(
    props.admin ?? true,
    setSelectedLog,
    context.logSource,
    false,
    false,
    context.logSource === 'usage'
  )
  const table = useReactTable({
    data: props.logs ?? [log],
    columns,
    getCoreRowModel: getCoreRowModel(),
    state: { columnVisibility: props.visibility ?? {} },
  })
  if (props.desktop) {
    const cell = table
      .getRowModel()
      .rows[0].getAllCells()
      .find((item) => item.column.id === 'prompt_tokens')
    if (!cell) throw new Error('The usage column is required for this fixture')
    return <>{flexRender(cell.column.columnDef.cell, cell.getContext())}</>
  }
  return (
    <>
      <button type='button' onClick={() => context.setSensitiveVisible(false)}>
        Hide sensitive data
      </button>
      <UsageLogsMobileList
        table={table}
        logCategory='common'
        isLoading={props.loading}
      />
      {selectedLog && (
        <DetailsDialog
          log={selectedLog.log}
          initialView={selectedLog.initialView}
          source={context.logSource}
          isAdmin={props.admin ?? true}
          isRoot={false}
          open
          onOpenChange={(open) => {
            if (!open) setSelectedLog(null)
          }}
        />
      )}
    </>
  )
}

afterEach(() => useAuthStore.getState().auth.reset())

function renderLogs(
  props: Parameters<typeof Fixture>[0] & { source?: 'usage' | 'test' } = {}
) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'viewer',
    role: props.admin === false ? 1 : 10,
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<Fixture {...props} />, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>
        <UsageLogsProvider source={props.source}>{children}</UsageLogsProvider>
      </QueryClientProvider>
    ),
  })
}

it('marks the active mobile request Processing without displaying final usage or cost', () => {
  const active = { ...log, type: 0, in_progress: true }
  renderLogs({ logs: [active] })
  expect(screen.getByText('Processing...')).toBeVisible()
  expect(
    screen.getByRole('button', { name: `Model: ${longName}` })
  ).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Token: backend-production-token' })
  ).toBeVisible()
  expect(
    screen.getByRole('term', { name: 'Total input tokens' }).nextElementSibling
  ).toHaveTextContent('—')
  expect(screen.queryByText('Consume')).not.toBeInTheDocument()
  expect(screen.queryByText('1,700')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', {
      name: /Token Breakdown|Billing Details|Cost breakdown|Details · #1/,
    })
  ).not.toBeInTheDocument()
})

it('keeps the selected personal request open from Details when a newer request arrives with the event column hidden', async () => {
  const user = userEvent.setup()
  const clipboard = vi
    .spyOn(navigator.clipboard, 'writeText')
    .mockResolvedValue()
  const stamp = '2025-12-31 23:59:59'
  const entry = {
    ...log,
    created_at: new Date('2025-12-31T23:59:59').valueOf() / 1000,
    request_id: 'personal-request',
    upstream_request_id: 'admin-only-upstream-id',
  }
  const opts = { admin: false, visibility: { content: false } }
  const view = renderLogs({ ...opts, logs: [entry] })
  const details = screen.getByRole('button', {
    name: /Details.*personal-request/,
  })
  expect(details).toHaveTextContent('Details')
  expect(screen.getByText('23:59:59').closest('button')).toBeNull()
  await user.click(details)
  const dialog = await screen.findByRole('dialog', { name: /Log Details/ })
  const newer = {
    ...entry,
    id: 2,
    request_id: 'newer-request',
    created_at: entry.created_at + 10,
  }
  view.rerender(<Fixture {...opts} logs={[newer, entry]} />)
  expect(within(dialog).getByText('personal-request')).toBeVisible()
  expect(within(dialog).queryByText('newer-request')).not.toBeInTheDocument()
  expect(
    within(dialog).queryByText('admin-only-upstream-id')
  ).not.toBeInTheDocument()
  await user.click(
    within(dialog).getByRole('button', { name: /Copy to clipboard.*Time/ })
  )
  expect(clipboard).toHaveBeenCalledWith(stamp)
})

it('shows model mismatch evidence when tapping the mobile model badge', async () => {
  const user = userEvent.setup()
  renderLogs({
    logs: [
      {
        ...log,
        other: JSON.stringify({
          response_model: {
            requested_model: longName,
            upstream_model: 'mapped-model',
            returned_model: 'unexpected-model',
          },
        }),
      },
    ],
  })
  await user.click(
    screen.getByRole('button', {
      name: `Model: ${longName}, Response model: unexpected-model`,
    })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Model' })
  expect(
    within(dialog).getByText('Response model: unexpected-model')
  ).toBeVisible()
  expect(within(dialog).getByText('mapped-model')).toBeVisible()
  expect(within(dialog).getByText('unexpected-model')).toBeVisible()
})

it('opens long channel text on tap and copies the complete value', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  renderLogs()
  await user.click(
    screen.getByRole('button', { name: `Channel: ${longName} #372` })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Channel' })
  expect(within(dialog).getByText(`${longName} #372`)).toHaveClass(
    '[overflow-wrap:anywhere]'
  )
  await user.click(
    within(dialog).getByRole('button', { name: 'Copy to clipboard' })
  )
  expect(copy).toHaveBeenCalledWith(`${longName} #372`)
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(
    screen.getByRole('button', { name: `Channel: ${longName} #372` })
  ).toHaveFocus()
})

it.each([false, true])(
  'copies the full mobile model name with the keyboard without opening details when there is no mapping or difference (response observed: %s)',
  async (observed) => {
    const user = userEvent.setup()
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    renderLogs({
      logs: [
        {
          ...log,
          other: observed
            ? JSON.stringify({
                response_model: {
                  requested_model: longName,
                  upstream_model: longName,
                  returned_model: longName,
                },
              })
            : log.other,
        },
      ],
    })
    const button = screen.getByRole('button', { name: `Model: ${longName}` })
    const modelText = within(button).getByText(longName)
    expect(modelText).toHaveClass(
      'line-clamp-2',
      'leading-5',
      '[overflow-wrap:anywhere]'
    )
    expect(modelText.closest('[data-slot="status-badge"]')).toHaveClass(
      'max-w-full',
      'min-h-6',
      'py-px'
    )
    button.focus()
    await user.keyboard('{Enter}')
    expect(copy).toHaveBeenCalledWith(longName)
    expect(
      screen.queryByRole('dialog', { name: 'Model' })
    ).not.toBeInTheDocument()
  }
)

it('hides sensitive names and disables full-text inspection when privacy is enabled', async () => {
  const user = userEvent.setup()
  renderLogs({ logs: [{ ...log, user_admin_nickname: 'Operations' }] })
  expect(screen.getByText('Operations')).toBeVisible()
  expect(
    screen.queryByText('Operations · production-admin')
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Hide sensitive data' }))
  expect(screen.queryByText('production-admin')).not.toBeInTheDocument()
  expect(screen.queryByText('Operations')).not.toBeInTheDocument()
  expect(screen.queryByTitle('production-admin')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Channel:/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Token:/ })
  ).not.toBeInTheDocument()
})

it('respects hidden columns and omits admin fields in the self view', () => {
  renderLogs({
    admin: false,
    visibility: { token_name: false, group: false, model_name: false },
  })
  expect(
    screen.queryByRole('button', {
      name: /Channel:|User:|Token:|Group:|Model:/,
    })
  ).not.toBeInTheDocument()
  expect(screen.queryByText('enterprise-production')).not.toBeInTheDocument()
})

it.each([
  ['username', 'User', 'production-admin'],
  ['token_name', 'Token', 'backend-production-token'],
  ['group', 'Group', 'enterprise-production'],
])('hides only the %s identity field on mobile', (column, label, value) => {
  renderLogs({ visibility: { [column]: false } })
  expect(
    screen.queryByRole('button', { name: `${label}: ${value}` })
  ).not.toBeInTheDocument()
  for (const [otherColumn, otherLabel, otherValue] of [
    ['username', 'User', 'production-admin'],
    ['token_name', 'Token', 'backend-production-token'],
    ['group', 'Group', 'enterprise-production'],
  ]) {
    if (otherColumn !== column) {
      expect(
        screen.getByRole('button', { name: `${otherLabel}: ${otherValue}` })
      ).toBeVisible()
    }
  }
})

it('keeps input, output and cache quantities readable without empty metric cells', () => {
  renderLogs()
  expect(
    screen.getByRole('term', { name: 'Total input tokens' }).nextElementSibling
  ).toHaveTextContent('1,700')
  expect(screen.getByRole('term', { name: 'Output Tokens' })).toBeVisible()
  expect(
    screen.getByRole('term', { name: 'Cache read tokens' }).nextElementSibling
  ).toHaveTextContent('300')
  expect(
    screen.getByRole('term', { name: 'Cache write tokens' }).nextElementSibling
  ).toHaveTextContent('200')
})

it('shows the established empty state when no logs exist', () => {
  renderLogs({ logs: [] })
  expect(screen.getByText('No Logs Found')).toBeVisible()
})

it.each([false, true])(
  'keeps first-token, total duration and output rate together for streaming=%s',
  (streaming) => {
    renderLogs({
      logs: [
        {
          ...log,
          is_stream: streaming,
          use_time: 58,
          other: JSON.stringify({ frt: 58000 }),
        },
      ],
    })
    const metrics = screen
      .getByRole('term', { name: 'Total time' })
      .closest('dl')
    if (!metrics) {
      throw new Error('The duration must appear with the request metrics')
    }
    expect(
      within(metrics).getByRole('term', { name: 'Time to first token' })
        .nextElementSibling
    ).toHaveTextContent(streaming ? '58.0s' : '—')
    expect(
      within(metrics).getByRole('term', { name: 'Total time' })
        .nextElementSibling
    ).toHaveTextContent('58.0s')
    expect(
      within(metrics).getByRole('term', { name: 'Output speed' })
        .nextElementSibling
    ).toHaveTextContent('14 t/s')
  }
)

it('retains the user avatar and model badge in the mobile summary', () => {
  renderLogs()
  expect(screen.getByText('P')).toBeVisible()
  const modelButton = screen.getByRole('button', { name: `Model: ${longName}` })
  expect(modelButton.querySelector('[data-slot="status-badge"]')).not.toBeNull()
})

it('omits unused token and throughput placeholders for async jobs', () => {
  renderLogs({
    logs: [
      {
        ...log,
        prompt_tokens: 0,
        completion_tokens: 0,
        other: JSON.stringify({ is_task: true }),
      },
    ],
  })
  expect(screen.getByText('Async')).toBeVisible()
  expect(
    screen.queryByRole('term', { name: 'Total input tokens' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('term', { name: 'Output Tokens' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('term', { name: 'Output speed' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('term', { name: 'Total time' }).nextElementSibling
  ).toHaveTextContent('1.5s')
})

it('labels a task whose result was returned in the response as synchronous', () => {
  renderLogs({
    logs: [
      {
        ...log,
        prompt_tokens: 0,
        completion_tokens: 0,
        other: JSON.stringify({ is_task: true, task_sync: true }),
      },
    ],
  })
  expect(screen.getByText('Sync')).toBeVisible()
  expect(screen.queryByText('Async')).not.toBeInTheDocument()
})

it('shows mapped model names in full when inspecting a mobile model badge', async () => {
  const user = userEvent.setup()
  renderLogs({
    logs: [
      {
        ...log,
        other: JSON.stringify({
          is_model_mapped: true,
          upstream_model_name:
            'provider-production-mapped-model-with-a-long-name',
        }),
      },
    ],
  })
  await user.click(screen.getByRole('button', { name: `Model: ${longName}` }))
  const dialog = await screen.findByRole('dialog', { name: 'Model' })
  expect(within(dialog).getByText('Actual Model')).toBeVisible()
  expect(
    within(dialog).getByText(
      'provider-production-mapped-model-with-a-long-name'
    )
  ).toBeVisible()
})

it('shows loading placeholders without displaying stale log fields', () => {
  renderLogs({ loading: true })
  expect(screen.getByRole('status', { name: 'Loading' })).toHaveAttribute(
    'aria-busy',
    'true'
  )
  expect(
    screen.queryByRole('button', { name: /^Model:/ })
  ).not.toBeInTheDocument()
})

it('labels historical successful requests as tests in the test records section', () => {
  renderLogs({ source: 'test' })
  expect(screen.getByText('Model test')).toBeVisible()
  expect(screen.queryByText('Consume')).not.toBeInTheDocument()
})

it('shows cache hit-rate progress on mobile and hides it with the token column', () => {
  const view = renderLogs()
  expect(
    screen.getByRole('progressbar', { name: 'Cache hit rate' })
  ).toHaveAttribute('aria-valuetext', '17.6%')
  view.unmount()
  renderLogs({ visibility: { prompt_tokens: false } })
  expect(
    screen.queryByRole('progressbar', { name: 'Cache hit rate' })
  ).not.toBeInTheDocument()
})

it('shows metered page usage when the request has no tokens', () => {
  renderLogs({
    logs: [
      {
        ...log,
        prompt_tokens: 0,
        completion_tokens: 0,
        other: JSON.stringify({ metered_usage: { unit: 'page', count: 2 } }),
      },
    ],
  })
  expect(screen.getByText('2 page')).toBeVisible()
  expect(screen.queryByText('Estimated usage')).not.toBeInTheDocument()
})

it.each([false, true])(
  'labels estimated metered usage in the desktop=%s list',
  (desktop) => {
    renderLogs({
      desktop,
      logs: [
        {
          ...log,
          type: 5,
          prompt_tokens: 0,
          completion_tokens: 0,
          other: JSON.stringify({
            metered_usage: { unit: 'page', count: 1000 },
            metered_usage_estimated: true,
          }),
        },
      ],
    })
    expect(screen.getByText('1,000 page')).toBeVisible()
    expect(screen.getByText('Estimated usage')).toBeVisible()
    expect(screen.queryByText('Billable usage:')).not.toBeInTheDocument()
  }
)

it('opens the shared pagination evidence from a mobile log', async () => {
  renderLogs({
    logs: [
      {
        ...log,
        prompt_tokens: 0,
        completion_tokens: 0,
        other: JSON.stringify({
          metered_usage: { unit: 'page', count: 2 },
          metered_usage_basis: 'local_pagination',
          metered_usage_engine: 'qpdf/12.2.0',
        }),
      },
    ],
  })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /Details · #1/ }))
  const dialog = await screen.findByRole('dialog', { name: /Log Details/ })
  await user.click(within(dialog).getByRole('tab', { name: 'Cost breakdown' }))
  await user.click(
    within(dialog).getByRole('button', { name: 'Billing parameters' })
  )
  expect(
    within(dialog).getByText('Locally counted document pages')
  ).toBeVisible()
  expect(within(dialog).getByText('Pagination engine')).toBeVisible()
  expect(within(dialog).getByText('qpdf/12.2.0')).toBeVisible()
})

it('summarizes a failed stream while keeping timing, retry-stop explanation, and the original error in details', async () => {
  renderLogs({
    admin: false,
    logs: [
      {
        ...log,
        type: 5,
        use_time: 0,
        is_stream: true,
        other: JSON.stringify({
          frt: 437,
          stream_status: {
            status: 'error',
            end_reason: 'upstream_error',
            end_error: 'Upstream temporarily unavailable',
          },
          retry_decision: {
            action: 'stop',
            reason: 'response_already_started',
          },
        }),
      },
    ],
  })
  expect(screen.getByText('437ms')).toBeVisible()
  expect(screen.getByText('Upstream unavailable')).toBeVisible()
  expect(
    screen.queryByText('Upstream temporarily unavailable')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText(
      'Response content has already been sent; retrying could duplicate output'
    )
  ).not.toBeInTheDocument()

  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /Details · #1/ }))
  const dialog = await screen.findByRole('dialog', { name: /Log Details/ })
  await user.click(
    within(dialog).getByRole('tab', { name: 'Technical details' })
  )
  expect(
    within(dialog).getByText('Upstream temporarily unavailable')
  ).toBeVisible()
  expect(
    within(dialog).getByText(
      'Response content has already been sent; retrying could duplicate output'
    )
  ).toBeVisible()
})
