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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { useState, type ReactNode } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { getOperationsSectionNavItems } from '../../operations/section-registry'
import type { UpdateOptionRequest } from '../../types'
import {
  parseRequestSourceRules,
  type RequestSourceRule,
} from '../request-source-rules'
import { RequestSourceRulesSection } from '../request-source-rules-section'

// jsdom has no drag geometry. Deliver the completed drag order at Motion's
// browser event boundary while retaining its real group/item rendering.
vi.mock('motion/react', async (importOriginal) => {
  const motion = await importOriginal<typeof import('motion/react')>()
  return {
    ...motion,
    Reorder: {
      ...motion.Reorder,
      Group: (props: {
        values: string[]
        onReorder: (ids: string[]) => void
        children?: ReactNode
        as?: 'ol'
        axis?: 'y'
        className?: string
        'aria-label'?: string
      }) => (
        <motion.Reorder.Group
          {...props}
          onPointerUp={(event) => {
            if (
              event.target === event.currentTarget &&
              props.values.length > 1
            ) {
              props.onReorder([...props.values.slice(1), props.values[0]])
            }
          }}
        />
      ),
    },
  }
})

const rule = (name: string): RequestSourceRule => ({
  enabled: true,
  name,
  conditions: [
    {
      header: 'User-Agent',
      match: 'prefix',
      value: 'codex/',
      case_sensitive: false,
    },
  ],
})

function Fixture(props: { value: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RequestSourceRulesSection value={props.value} />
      </SettingsPageProvider>
    </>
  )
}

function renderRules(value: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <Fixture value={value} />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('lists source recognition beside log maintenance in operations settings', async () => {
  const translations = createInstance()
  await translations.init({ lng: 'en', resources: {} })
  const entries = getOperationsSectionNavItems(translations.t)
  const logs = entries.findIndex((entry) => entry.url.endsWith('/logs'))
  expect(entries[logs + 1]).toMatchObject({
    url: '/system-settings/operations/request-sources',
    title: 'Request source recognition',
  })
})

test('saves the complete ordered rule list with all conditions and switch values preserved', async () => {
  const user = userEvent.setup()
  const alpha = rule('Alpha')
  const beta = rule('Beta')
  beta.conditions.push({
    header: 'X-Client-Name',
    match: 'exact',
    value: 'CI',
    case_sensitive: true,
  })
  renderRules(JSON.stringify([alpha, beta]))
  await user.click(screen.getByRole('button', { name: 'Move Beta up' }))
  const rows = within(
    screen.getByRole('list', { name: 'Source rule priority' })
  ).getAllByRole('listitem')
  expect(
    within(rows[0]).getByRole('textbox', { name: 'Source display name' })
  ).toHaveValue('Beta')
  expect(
    within(rows[0]).getAllByRole('group', { name: 'Source match condition' })
  ).toHaveLength(2)
  await user.click(within(rows[0]).getByRole('switch', { name: 'Enabled' }))
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  expect(api.put).toHaveBeenCalledWith('/api/option/', {
    key: 'RequestSourceRules',
    value: JSON.stringify([{ ...beta, enabled: false }, alpha]),
  })
})

test('adds conditions, resets unsaved edits, and supports keyboard ordering without changing rule content', async () => {
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha'), rule('Beta')]))
  const rows = screen.getAllByRole('listitem')
  await user.click(
    within(rows[0]).getByRole('button', { name: 'Add condition' })
  )
  expect(
    within(rows[0]).getAllByRole('group', { name: 'Source match condition' })
  ).toHaveLength(2)
  await user.click(screen.getByRole('button', { name: 'Reset' }))
  expect(
    screen.getAllByRole('group', { name: 'Source match condition' })
  ).toHaveLength(2)
  screen.getByRole('button', { name: 'Drag Beta to reorder' }).focus()
  await user.keyboard('{ArrowUp}')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  expect(
    JSON.parse(
      String((vi.mocked(api.put).mock.calls[0][1] as UpdateOptionRequest).value)
    )
  ).toEqual([rule('Beta'), rule('Alpha')])
})

test('saves a complete non-adjacent drag permutation without mixing rule conditions', async () => {
  const user = userEvent.setup()
  const original = ['Alpha', 'Beta', 'Gamma', 'Delta'].map((name) => ({
    ...rule(name),
    conditions: [{ ...rule(name).conditions[0], value: name }],
  }))
  renderRules(JSON.stringify(original))
  fireEvent.pointerUp(
    screen.getByRole('list', { name: 'Source rule priority' })
  )
  expect(
    screen
      .getAllByRole('textbox', { name: 'Source display name' })
      .map((input) => (input as HTMLInputElement).value)
  ).toEqual(['Beta', 'Gamma', 'Delta', 'Alpha'])
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  expect(
    JSON.parse(
      String((vi.mocked(api.put).mock.calls[0][1] as UpdateOptionRequest).value)
    )
  ).toEqual([...original.slice(1), original[0]])
})

test('adds and deletes rules while allowing an intentionally empty list to use built-in recognition', async () => {
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha')]))
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  expect(
    screen.getAllByRole('textbox', { name: 'Source display name' })
  ).toHaveLength(2)
  await user.click(screen.getByRole('button', { name: 'Remove Source rule 2' }))
  await user.click(screen.getByRole('button', { name: 'Remove Alpha' }))
  expect(
    screen.getByText(
      'No custom rules. Built-in source recognition remains active.'
    )
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'RequestSourceRules',
      value: '[]',
    })
  )
})

test.each([
  '{broken',
  'null',
  '{}',
  JSON.stringify([{ ...rule('Alpha'), unknown: true }]),
])(
  'locks editing for unreadable saved configuration %s without overwriting it',
  async (raw) => {
    renderRules(raw)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Saved source rules could not be read.'
    )
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Add rule' })
    ).not.toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('rejects a sensitive header in the editor before saving', async () => {
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha')]))
  const header = screen.getByRole('textbox', { name: 'HTTP header name' })
  fireEvent.change(header, { target: { value: 'X-API_Key' } })
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(header).toHaveAttribute('aria-invalid', 'true'))
  expect(api.put).not.toHaveBeenCalled()
})

test('lets the server validate RE2 and retains the edited rule when saving fails', async () => {
  const user = userEvent.setup()
  const regex = rule('Alpha')
  regex.conditions[0].match = 'regex'
  renderRules(JSON.stringify([regex]))
  const input = screen.getByRole('textbox', { name: 'Header match value' })
  fireEvent.change(input, { target: { value: '(?i)^codex/' } })
  vi.mocked(api.put).mockRejectedValueOnce(
    new Error('Invalid regular expression')
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  expect(input).toHaveValue('(?i)^codex/')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(2))
  expect(
    JSON.parse(
      String((vi.mocked(api.put).mock.calls[1][1] as UpdateOptionRequest).value)
    )[0].conditions[0].value
  ).toBe('(?i)^codex/')
})

test('enforces UTF-8 limits, list bounds, and forbidden headers without compiling regex in JavaScript', async () => {
  const translations = createInstance()
  await translations.init({ lng: 'en', resources: {} })
  const valid = rule('中文来源')
  valid.conditions[0] = {
    header: 'User-Agent',
    match: 'regex',
    value: '(?i)^codex/',
    case_sensitive: false,
  }
  expect(
    parseRequestSourceRules(JSON.stringify([valid]), translations.t)
  ).toEqual([valid])
  for (const invalid of [
    [{ ...valid, name: '中'.repeat(22) }],
    [{ ...valid, name: 'sk-secret' }],
    [{ ...valid, name: 'Bearer custom' }],
    [{ ...valid, conditions: [] }],
    [{ ...valid, conditions: Array(9).fill(valid.conditions[0]) }],
    [
      {
        ...valid,
        conditions: [{ ...valid.conditions[0], value: '中'.repeat(171) }],
      },
    ],
    Array(101).fill(valid),
    Array(100).fill({
      ...valid,
      conditions: Array(8).fill({
        ...valid.conditions[0],
        value: 'x'.repeat(512),
      }),
    }),
    ...[
      'Authorization',
      'Cookie',
      'X-API_Key',
      'X-Access-Token',
      'Proxy-Authentication',
      'X-Secret',
      ' User-Agent',
    ].map((header) => [
      { ...valid, conditions: [{ ...valid.conditions[0], header }] },
    ]),
  ]) {
    expect(
      parseRequestSourceRules(JSON.stringify(invalid), translations.t)
    ).toBeNull()
  }
})
