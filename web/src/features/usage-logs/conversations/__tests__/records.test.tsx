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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { ConversationRecordsPage } from '..'
import type { RequestTraceLeg } from '../../types'

afterEach(() => useAuthStore.setState(useAuthStore.getInitialState(), true))

function record(id: string) {
  return {
    conversation_id: id,
    parent_id: '',
    agent_name: '',
    title: `Discussion ${id}`,
    model_name: 'model',
    user_id: 9,
    started_at: 100,
    updated_at: 101,
    turn_count: 2,
  }
}

function turn(id: string, time: number) {
  return {
    conversation_id: 'A',
    parent_id: '',
    agent_name: '',
    started_at_ms: time * 1000,
    trace_id: id,
    request_id: id,
    model_name: id,
    created_at: time,
  }
}

function leg(seq: number, body: unknown, truncated = false): RequestTraceLeg {
  return {
    seq,
    attempt: 0,
    direction: seq === 1 ? 'client_request' : 'client_response',
    channel_id: 1,
    format: 'openai',
    method: 'POST',
    url: '',
    status: 200,
    headers: {},
    body: JSON.stringify(body),
    body_size: 10,
    truncated,
    has_object: false,
  }
}

function admin() {
  useAuthStore
    .getState()
    .auth.setUser({ id: 5, role: ROLE.ADMIN, username: 'viewer' })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ConversationRecordsPage />
    </QueryClientProvider>
  )
}

test('loads only requested captures, displays new turns once, and pins the selected conversation across list refreshes', async () => {
  const user = userEvent.setup()
  let listing = [record('A')]
  const loaded: string[] = []
  const messages = [
    { role: 'user', content: 'Earlier question' },
    { role: 'assistant', content: 'Earlier answer' },
    { role: 'user', content: 'Latest question' },
  ]
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data
    const params = config?.params as {
      cursor?: string
      trace_id?: string
      conversation_id?: string
    }
    if (url === '/api/log/conversations') {
      data = {
        items: params.cursor ? [record('B')] : listing,
        next_cursor: params.cursor ? '' : 'cursor',
      }
    } else if (url === '/api/log/conversations/turns') {
      expect(params.conversation_id).toBe('A')
      data = {
        items: [turn('Latest', 102), turn('Earlier', 101), turn('Oldest', 100)],
        next_cursor: '',
      }
    } else if (url === '/api/log/trace') {
      const latest = params.trace_id === 'Latest'
      loaded.push(params.trace_id ?? '')
      data = {
        legs: [
          leg(1, { messages: latest ? messages : messages.slice(0, 1) }),
          leg(2, {
            choices: [
              {
                message: {
                  content: latest ? 'Latest answer' : 'Earlier answer',
                },
              },
            ],
          }),
        ],
      }
    } else throw new Error(url)
    return { data: { success: true, data } }
  })
  admin()
  await screen.findByRole('button', { name: /Discussion A/ })
  expect(loaded).toEqual([])
  await user.click(
    screen.getByRole('button', { name: 'Load more conversations' })
  )
  await screen.findByRole('button', { name: /Discussion B/ })
  await user.click(screen.getByRole('button', { name: /Discussion A/ }))
  expect(await screen.findByText('Latest answer')).toBeVisible()
  expect(screen.getAllByText('Latest question')).toHaveLength(1)
  expect(screen.queryByText('Earlier question')).not.toBeInTheDocument()
  expect(loaded).toEqual(['Latest', 'Earlier'])
  listing = [record('C')]
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByRole('button', { name: /Discussion C/ })
  expect(screen.getByRole('heading', { name: 'Discussion A' })).toBeVisible()
  expect(screen.getByText('Latest answer')).toBeVisible()
  expect(loaded).toEqual(['Latest', 'Earlier'])
})

test('cancels stale reads on selection change and identifies missing and truncated capture', async () => {
  const user = userEvent.setup()
  let oldSignal: AbortSignal | undefined
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    const params = config?.params as {
      conversation_id?: string
      trace_id?: string
    }
    if (url === '/api/log/conversations') {
      return {
        data: {
          success: true,
          data: { items: [record('A'), record('B')], next_cursor: '' },
        },
      }
    }
    if (url === '/api/log/conversations/turns') {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                ...turn(params.conversation_id ?? '', 100),
                conversation_id: params.conversation_id,
              },
            ],
            next_cursor: '',
          },
        },
      }
    }
    if (params.trace_id === 'A') {
      oldSignal = config?.signal as AbortSignal
      return new Promise((_resolve, reject) =>
        oldSignal?.addEventListener(
          'abort',
          () => reject(new DOMException('Aborted', 'AbortError')),
          { once: true }
        )
      )
    }
    return {
      data: {
        success: true,
        data: {
          legs: [
            leg(
              1,
              { messages: [{ role: 'user', content: 'Retained fragment' }] },
              true
            ),
          ],
        },
      },
    }
  })
  admin()
  await user.click(await screen.findByRole('button', { name: /Discussion A/ }))
  await waitFor(() => expect(oldSignal).toBeDefined())
  await user.click(screen.getByRole('button', { name: /Discussion B/ }))
  expect(await screen.findByText('Retained fragment')).toBeVisible()
  expect(oldSignal?.aborted).toBe(true)
  expect(
    screen.getByText('Some messages or replies were not captured.')
  ).toBeVisible()
  expect(
    screen.getByText(
      'This payload was truncated. The conversation may be incomplete.'
    )
  ).toBeVisible()
})

test('applies combined filters to the full list and carries them through sorted pagination', async () => {
  const user = userEvent.setup()
  const received: Record<string, unknown>[] = []
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data
    if (url === '/api/log/conversations') {
      const params = (config?.params ?? {}) as Record<string, unknown>
      received.push(params)
      const id = params.cursor ? 'Next' : 'First'
      data = { items: [record(id)], next_cursor: params.cursor ? '' : 'page-2' }
    } else if (url === '/api/log/conversations/clients') {
      data = ['Codex CLI', 'Claude Code']
    } else if (url.startsWith('/api/user/')) {
      data = { items: [{ id: 18, username: 'alice' }] }
    } else throw new Error(url)
    return { data: { success: true, data } }
  })
  admin()
  await screen.findByRole('button', { name: /Discussion First/ })
  await user.click(
    screen.getByRole('button', { name: 'Load more conversations' })
  )
  await screen.findByRole('button', { name: /Discussion Next/ })
  await user.click(screen.getByRole('button', { name: 'Filter' }))
  const userInput = screen.getByRole('combobox', { name: 'User' })
  await user.click(userInput)
  await user.click(await screen.findByRole('option', { name: 'alice' }))
  await user.click(screen.getByRole('combobox', { name: 'Request client' }))
  await user.click(await screen.findByRole('option', { name: 'Codex CLI' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Model Name' }),
    'test-model'
  )
  await user.type(
    screen.getByRole('spinbutton', { name: 'Minimum turns' }),
    '3'
  )
  await user.type(
    screen.getByRole('spinbutton', { name: 'Maximum turns' }),
    '1'
  )
  expect(screen.getByRole('button', { name: 'Apply Filters' })).toBeDisabled()
  await user.type(
    screen.getByRole('spinbutton', { name: 'Maximum turns' }),
    '0'
  )
  await user.click(screen.getByRole('button', { name: 'Apply Filters' }))
  await waitFor(() =>
    expect(received.at(-1)).toMatchObject({
      cursor: '',
      user_id: 18,
      client_tool: 'Codex CLI',
      model_name: 'test-model',
      min_turns: 3,
      max_turns: 10,
    })
  )
  expect(
    screen.queryByRole('button', { name: /Discussion Next/ })
  ).not.toBeInTheDocument()
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Sort' }),
    'turn_count:asc'
  )
  await screen.findByRole('button', { name: /Discussion First/ })
  await user.click(
    screen.getByRole('button', { name: 'Load more conversations' })
  )
  await waitFor(() =>
    expect(received.at(-1)).toMatchObject({
      cursor: 'page-2',
      user_id: 18,
      sort_by: 'turn_count',
      sort_order: 'asc',
      client_tool: 'Codex CLI',
      min_turns: 3,
      max_turns: 10,
    })
  )
  await user.click(screen.getByRole('button', { name: /Filter/ }))
  await user.click(screen.getByRole('button', { name: 'Clear filters' }))
  await waitFor(() =>
    expect(received.at(-1)).toEqual({
      cursor: '',
      limit: 30,
      sort_by: 'turn_count',
      sort_order: 'asc',
    })
  )
})

test('applies the greeting-only switch independently of round count and clears the previous reader', async () => {
  const user = userEvent.setup()
  const requests: Record<string, unknown>[] = []
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url === '/api/log/conversations') {
      const params = (config?.params ?? {}) as Record<string, unknown>
      requests.push(params)
      const items = params.hide_greetings
        ? [record('One question')]
        : [record('Hello'), record('One question')]
      return { data: { success: true, data: { items, next_cursor: '' } } }
    }
    if (url === '/api/log/conversations/turns') {
      return { data: { success: true, data: { items: [], next_cursor: '' } } }
    }
    throw new Error(url)
  })
  admin()
  await user.click(
    await screen.findByRole('button', { name: /Discussion Hello/ })
  )
  expect(
    screen.getByRole('heading', { name: 'Discussion Hello' })
  ).toBeVisible()
  await user.click(
    screen.getByRole('switch', { name: 'Hide greeting-only conversations' })
  )
  expect(
    await screen.findByRole('button', { name: /Discussion One question/ })
  ).toBeVisible()
  expect(
    screen.queryByRole('heading', { name: 'Discussion Hello' })
  ).not.toBeInTheDocument()
  expect(requests.at(-1)).toMatchObject({ hide_greetings: true, cursor: '' })
  expect(requests.at(-1)).not.toHaveProperty('min_turns')
  expect(
    screen.getByRole('switch', { name: 'Hide greeting-only conversations' })
  ).toBeChecked()
  await user.click(
    screen.getByRole('switch', { name: 'Hide greeting-only conversations' })
  )
  expect(
    await screen.findByRole('button', { name: /Discussion Hello/ })
  ).toBeVisible()
})

test('distinguishes an unrecognized client from a custom client name and from all clients', async () => {
  const user = userEvent.setup()
  const queries: Record<string, unknown>[] = []
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url.endsWith('/clients')) return { data: { success: true, data: [] } }
    queries.push((config?.params ?? {}) as Record<string, unknown>)
    return { data: { success: true, data: { items: [], next_cursor: '' } } }
  })
  admin()
  await screen.findByText('No saved conversations')
  await user.click(screen.getByRole('button', { name: 'Filter' }))
  await user.click(screen.getByRole('combobox', { name: 'Request client' }))
  await user.click(screen.getByRole('option', { name: 'Unidentified source' }))
  await user.click(screen.getByRole('button', { name: 'Apply Filters' }))
  await waitFor(() => expect(queries.at(-1)?.client_tool_unknown).toBe(true))
  expect(queries.at(-1)?.client_tool).toBeUndefined()
  await screen.findByText('No records found. Try adjusting your filters.')
  await user.click(screen.getByRole('button', { name: /Filter/ }))
  const input = screen.getByRole('combobox', { name: 'Request client' })
  await user.clear(input)
  await user.type(input, 'Internal SDK')
  await user.click(screen.getByRole('button', { name: 'Apply Filters' }))
  await waitFor(() => expect(queries.at(-1)?.client_tool).toBe('Internal SDK'))
  expect(queries.at(-1)?.client_tool_unknown).toBeUndefined()
})
