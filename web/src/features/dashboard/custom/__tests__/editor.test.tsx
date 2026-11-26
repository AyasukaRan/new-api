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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useState } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { CustomDashboardEditor } from '../editor'
import { DashboardGrid } from '../grid'
import { placeDashboardPanel } from '../layout'
import { DashboardPanelSettings } from '../panel-settings'
import type { DashboardConfig, DashboardPanel } from '../types'

const panel: DashboardPanel = {
  id: 'requests',
  title: 'Request volume',
  source: 'usage',
  metric: 'requests',
  chart: 'stat',
  dimension: 'model',
  filters: {},
  layout: { x: 0, y: 0, w: 6, h: 4 },
}
const time = { range: '24h', granularity: 'hour', refresh: 0 } as const
let config: DashboardConfig
let client: QueryClient

beforeEach(() => {
  config = {
    version: 1,
    active: 'first',
    boards: [
      {
        id: 'first',
        name: 'Operations',
        time,
        panels: [structuredClone(panel)],
      },
      { id: 'second', name: 'Personal', time, panels: [] },
    ],
  }
  client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/user/self/dashboards'
          ? { revision: 3, config: structuredClone(config) }
          : [],
    },
  }))
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})
function renderEditor() {
  const exit = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <CustomDashboardEditor userId={7} isAdmin={false} onExit={exit} />
    </QueryClientProvider>
  )
  return exit
}

test('board edits stay in an isolated draft and cancel restores saved names and cards', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put')
  renderEditor()
  await user.click(
    await screen.findByRole('button', { name: 'Edit dashboard' })
  )
  await user.click(screen.getByRole('button', { name: 'Rename dashboard' }))
  await user.clear(screen.getByRole('textbox', { name: 'Name' }))
  await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Draft name')
  await user.click(
    within(screen.getByRole('dialog')).getByRole('button', { name: 'Apply' })
  )
  expect(screen.getByRole('combobox', { name: 'Dashboard' })).toHaveTextContent(
    'Draft name'
  )
  await user.click(
    screen.getByRole('button', { name: 'Remove panel Request volume' })
  )
  expect(screen.getByText('No panels')).toBeVisible()
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Discard',
    })
  )
  expect(screen.getByRole('combobox', { name: 'Dashboard' })).toHaveTextContent(
    'Operations'
  )
  expect(screen.getByRole('region', { name: 'Request volume' })).toBeVisible()
})

test('multiple boards save together with the starting revision while conflict preserves the full draft', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockRejectedValueOnce({
      response: { status: 409, data: { message: 'Conflict' } },
    })
    .mockImplementation(async (_url, body) => ({
      data: {
        success: true,
        data: {
          revision: 4,
          config: (body as { config: DashboardConfig }).config,
        },
      },
    }))
  renderEditor()
  await user.click(
    await screen.findByRole('button', { name: 'Edit dashboard' })
  )
  await user.click(screen.getByRole('button', { name: 'New dashboard' }))
  await user.type(screen.getByRole('textbox', { name: 'Name' }), 'New board')
  await user.click(
    within(screen.getByRole('dialog')).getByRole('button', { name: 'Apply' })
  )
  await user.click(screen.getByRole('button', { name: 'Save dashboard' }))
  expect(
    await screen.findByText(
      'This dashboard changed elsewhere. Your draft is preserved. Reload the saved version or keep editing.'
    )
  ).toBeVisible()
  expect(screen.getByRole('combobox', { name: 'Dashboard' })).toHaveTextContent(
    'New board'
  )
  const first = put.mock.calls[0][1] as {
    revision: number
    config: DashboardConfig
  }
  expect(first.revision).toBe(3)
  expect(first.config.boards.map((board) => board.name)).toEqual([
    'Operations',
    'Personal',
    'New board',
  ])
  await user.click(screen.getByRole('button', { name: 'Save dashboard' }))
  await screen.findByRole('button', { name: 'Edit dashboard' })
  expect(put.mock.calls[1][1]).toEqual(first)
})

test('leaving edit mode asks before discarding and global view time changes do not save configuration', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put')
  const exit = renderEditor()
  await screen.findByRole('button', { name: 'Edit dashboard' })
  await user.click(screen.getByRole('combobox', { name: 'Time range' }))
  await user.click(screen.getByRole('option', { name: 'Last 7 days' }))
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Edit dashboard' }))
  await user.click(screen.getByRole('button', { name: 'Built-in dashboard' }))
  expect(exit).not.toHaveBeenCalled()
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Discard',
    })
  )
  expect(exit).toHaveBeenCalledOnce()
})

test('panel settings limit non-admin sources and preserve a supported independent time override', async () => {
  const user = userEvent.setup()
  const apply = vi.fn()
  render(
    <DashboardPanelSettings
      panel={panel}
      globalTime={time}
      isAdmin={false}
      onApply={apply}
      onClose={() => undefined}
    />
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Model Name' }),
    ' model-a '
  )
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(apply).toHaveBeenLastCalledWith(
    expect.objectContaining({ filters: { model: 'model-a' } })
  )
  await user.click(screen.getByRole('combobox', { name: 'Data source' }))
  expect(
    screen.queryByRole('option', { name: 'Live gateway metrics' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('option', { name: 'Call sources' }))
  expect(
    screen.queryByRole('textbox', { name: 'Model Name' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('textbox', { name: 'Username' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('combobox', { name: 'Group by' })).toHaveTextContent(
    'Total'
  )
  await user.click(screen.getByRole('switch', { name: 'Use panel time range' }))
  await user.click(screen.getByRole('combobox', { name: 'Time range' }))
  await user.click(screen.getByRole('option', { name: 'Yesterday' }))
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(apply).toHaveBeenCalledWith(
    expect.objectContaining({
      source: 'sources',
      dimension: 'total',
      time: { ...time, range: 'yesterday' },
    })
  )
})

test('grid keyboard movement and resizing keep cards within columns and move collisions down', async () => {
  const second = {
    ...panel,
    id: 'tokens',
    title: 'Tokens',
    layout: { ...panel.layout, x: 6 },
  }
  const onChange = vi.fn()
  function GridFixture() {
    const [panels, setPanels] = useState([panel, second])
    return (
      <DashboardGrid
        panels={panels}
        time={time}
        editing
        isAdmin={false}
        userId={7}
        onChange={(next) => {
          onChange(next)
          setPanels(next)
        }}
        onConfigure={() => undefined}
        onRemove={() => undefined}
      />
    )
  }
  render(
    <QueryClientProvider client={client}>
      <GridFixture />
    </QueryClientProvider>
  )
  fireEvent.keyDown(
    screen.getByRole('button', { name: 'Move panel Request volume' }),
    { key: 'ArrowRight' }
  )
  expect(onChange).toHaveBeenLastCalledWith([
    expect.objectContaining({ layout: { x: 1, y: 0, w: 6, h: 4 } }),
    expect.objectContaining({ layout: { x: 6, y: 4, w: 6, h: 4 } }),
  ])
  fireEvent.keyDown(
    screen.getByRole('button', { name: 'Resize panel Request volume' }),
    { key: 'ArrowRight' }
  )
  expect(
    screen.getByRole('region', { name: 'Request volume' }).style.gridColumn
  ).toBe('2 / span 7')
  const clamped = placeDashboardPanel([panel, second], panel.id, {
    x: 99,
    y: -2,
    w: 50,
    h: 1,
  })
  expect(clamped[0].layout).toEqual({ x: 0, y: 0, w: 12, h: 3 })
  expect(clamped[1].layout.y).toBe(3)
})

test('a query refresh while editing never overwrites the current draft', async () => {
  const user = userEvent.setup()
  renderEditor()
  await user.click(
    await screen.findByRole('button', { name: 'Edit dashboard' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Remove panel Request volume' })
  )
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['dashboard-config', 7] })
  })
  await waitFor(() => expect(screen.getByText('No panels')).toBeVisible())
  expect(
    screen.queryByRole('region', { name: 'Request volume' })
  ).not.toBeInTheDocument()
})

test('the last dashboard cannot be deleted and role changes fetch a separate configuration', async () => {
  config.boards = config.boards.slice(0, 1)
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <CustomDashboardEditor userId={7} isAdmin onExit={() => undefined} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit dashboard' })
  )
  expect(
    screen.getByRole('button', { name: 'Delete dashboard' })
  ).toBeDisabled()
  await user.click(
    screen.getByRole('button', { name: 'Remove panel Request volume' })
  )
  expect(screen.getByText('No panels')).toBeVisible()
  view.rerender(
    <QueryClientProvider client={client}>
      <CustomDashboardEditor
        userId={7}
        isAdmin={false}
        onExit={() => undefined}
      />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Edit dashboard' })
  expect(
    vi
      .mocked(api.get)
      .mock.calls.filter(([url]) => url === '/api/user/self/dashboards')
  ).toHaveLength(2)
  expect(screen.getByRole('region', { name: 'Request volume' })).toBeVisible()
})

test('StrictMode remount loads its own request after the first observer is canceled', async () => {
  vi.mocked(api.get).mockRestore()
  const originalAdapter = api.defaults.adapter
  const completed = vi.fn()
  api.defaults.adapter = async (request) => {
    completed(request.url)
    return {
      data: {
        success: true,
        data:
          request.url === '/api/user/self/dashboards'
            ? { revision: 3, config: structuredClone(config) }
            : [],
      },
      status: 200,
      statusText: 'OK',
      headers: {},
      config: request,
    }
  }
  try {
    render(
      <StrictMode>
        <QueryClientProvider client={client}>
          <CustomDashboardEditor
            userId={7}
            isAdmin={false}
            onExit={() => undefined}
          />
        </QueryClientProvider>
      </StrictMode>
    )
    expect(
      await screen.findByRole('button', { name: 'Edit dashboard' })
    ).toBeVisible()
    expect(screen.getByRole('region', { name: 'Request volume' })).toBeVisible()
    expect(completed).toHaveBeenCalledWith('/api/user/self/dashboards')
    expect(
      screen.queryByText('Failed to load dashboard')
    ).not.toBeInTheDocument()
  } finally {
    api.defaults.adapter = originalAdapter
  }
})
