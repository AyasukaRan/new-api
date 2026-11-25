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
import { createInstance } from 'i18next'
import { useState } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { UpdateOptionRequest } from '../../types'
import {
  parseErrorRecognitionRules,
  type ErrorRecognitionRule,
} from '../error-recognition-rules'
import { ErrorRecognitionSection } from '../error-recognition-section'

const rule = (name: string): ErrorRecognitionRule => ({
  name,
  label: name,
  enabled: true,
  match: 'contains',
  pattern: `${name} error`,
  status_codes: [400, 413],
})
const clients: QueryClient[] = []

function Fixture(props: { value: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <ErrorRecognitionSection value={props.value} />
      </SettingsPageProvider>
    </>
  )
}

function renderRules(
  value: string,
  translations?: ReturnType<typeof createInstance>
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const fixture = <Fixture value={value} />
  return render(
    <QueryClientProvider client={client}>
      {translations ? (
        <I18nextProvider i18n={translations}>{fixture}</I18nextProvider>
      ) : (
        fixture
      )}
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test.each([
  '',
  '{broken',
  '{}',
  JSON.stringify([{ ...rule('bad'), enabled: 'yes' }]),
  JSON.stringify([rule('same'), rule('same')]),
])(
  'blocks editing unreadable stored configuration %s without overwriting it',
  (value) => {
    renderRules(value)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Saved error rules could not be read.'
    )
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Add rule' })
    ).not.toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('keeps saved rule details collapsed and preserves edits, disable state and priority when saving', async () => {
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha'), rule('Beta')]))
  expect(
    screen.queryByRole('textbox', { name: 'Error pattern' })
  ).not.toBeInTheDocument()
  const rows = screen.getAllByRole('listitem')
  await user.click(
    within(rows[1]).getByRole('button', { name: 'Edit error rule' })
  )
  await user.clear(screen.getByRole('textbox', { name: 'Short error label' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Short error label' }),
    'Second failure'
  )
  await user.click(within(rows[1]).getByRole('switch', { name: 'Enabled' }))
  screen.getByRole('button', { name: 'Drag Second failure to reorder' }).focus()
  await user.keyboard('{ArrowUp}')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'ErrorRecognitionRules',
      value: JSON.stringify([
        { ...rule('Beta'), label: 'Second failure', enabled: false },
        rule('Alpha'),
      ]),
    })
  )
})

test('validates comma-separated status codes while adding a rule and keeps its editor open when changing the ID', async () => {
  vi.stubGlobal('crypto', {
    getRandomValues: crypto.getRandomValues.bind(crypto),
  })
  const user = userEvent.setup()
  renderRules('[]')
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  await user.clear(screen.getByRole('textbox', { name: 'Rule ID' }))
  await user.type(screen.getByRole('textbox', { name: 'Rule ID' }), 'context')
  await user.type(
    screen.getByRole('textbox', { name: 'Short error label' }),
    'Context length exceeded'
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Error pattern' }),
    'maximum context length'
  )
  const codes = screen.getByRole('textbox', { name: 'Status codes (optional)' })
  await user.type(codes, '200')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText(
      'Enter comma-separated status codes between 400 and 599, or leave empty.'
    )
  ).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
  await user.clear(codes)
  await user.type(codes, '400, 413')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  const saved = JSON.parse(
    String((vi.mocked(api.put).mock.calls[0][1] as UpdateOptionRequest).value)
  )
  expect(saved).toEqual([
    {
      name: 'context',
      label: 'Context length exceeded',
      enabled: true,
      match: 'contains',
      pattern: 'maximum context length',
      status_codes: [400, 413],
    },
  ])
})

test('shows a translated built-in label without rewriting its stored key when another field changes', async () => {
  const translations = createInstance()
  await translations.init({
    lng: 'zh',
    resources: {
      zh: { translation: { 'Context length exceeded': '上下文超长' } },
    },
  })
  const original = { ...rule('context'), label: 'Context length exceeded' }
  const user = userEvent.setup()
  renderRules(JSON.stringify([original]), translations)
  await user.click(screen.getByRole('button', { name: 'Edit error rule' }))
  expect(
    screen.getByRole('textbox', { name: 'Short error label' })
  ).toHaveValue('上下文超长')
  await user.click(screen.getByRole('switch', { name: 'Enabled' }))
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'ErrorRecognitionRules',
      value: JSON.stringify([{ ...original, enabled: false }]),
    })
  )
})

test('allows deleting every rule and saves an intentional empty array', async () => {
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha')]))
  await user.click(screen.getByRole('button', { name: 'Remove Alpha' }))
  expect(
    screen.getByText('No error rules. Errors will use the general fallback.')
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'ErrorRecognitionRules',
      value: '[]',
    })
  )
})

test('submits the unsaved rule draft to the server for matching without changing settings', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({
      data: {
        success: true,
        data: {
          label: 'Context length exceeded',
          matched: true,
          rule_id: 'Alpha',
        },
      },
    })
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha')]))
  await user.click(screen.getByRole('button', { name: 'Edit error rule' }))
  await user.clear(screen.getByRole('textbox', { name: 'Error pattern' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Error pattern' }),
    'context length'
  )
  await user.click(screen.getByRole('tab', { name: 'Test match' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Original error' }),
    'maximum context length exceeded'
  )
  await user.type(
    screen.getByRole('spinbutton', { name: 'Status Code' }),
    '400'
  )
  await user.click(screen.getByRole('button', { name: 'Test match' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/option/error_recognition_rules/preview',
      {
        rules: [{ ...rule('Alpha'), pattern: 'context length' }],
        error: 'maximum context length exceeded',
        status_code: 400,
      }
    )
  )
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Context length exceeded'
  )
  expect(api.put).not.toHaveBeenCalled()
})

test('keeps a rejected preview error and the invalid draft instead of treating it as an unmatched result', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({
      data: {
        success: false,
        message: 'Status code must be between 400 and 599',
      },
    })
  const user = userEvent.setup()
  renderRules(JSON.stringify([rule('Alpha')]))
  await user.click(screen.getByRole('button', { name: 'Edit error rule' }))
  await user.clear(
    screen.getByRole('textbox', { name: 'Status codes (optional)' })
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Status codes (optional)' }),
    '200'
  )
  await user.click(screen.getByRole('tab', { name: 'Test match' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Original error' }),
    'upstream failed'
  )
  await user.click(screen.getByRole('button', { name: 'Test match' }))
  expect(
    await screen.findByText('Status code must be between 400 and 599')
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/option/error_recognition_rules/preview',
    expect.objectContaining({
      rules: [{ ...rule('Alpha'), status_codes: [200] }],
    })
  )
  expect(screen.getByRole('textbox', { name: 'Original error' })).toHaveValue(
    'upstream failed'
  )
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Rules' }))
  expect(
    screen.getByRole('textbox', { name: 'Status codes (optional)' })
  ).toHaveValue('200')
  expect(api.put).not.toHaveBeenCalled()
})

const sample = {
  pattern: 'unrecognized upstream failure <number>',
  suggested_pattern: 'unrecognized upstream failure',
  status_code: 503,
  count: 2,
  last_seen: 1795600000,
  label: 'Request failed',
  matched: false,
}
const inventoryResponse = (
  groups: (typeof sample)[],
  reviewed: number,
  next_cursor = ''
) => ({ data: { success: true, data: { groups, reviewed, next_cursor } } })

test('combines scanned history batches, filters unmatched errors, and creates a draft from the safe suggestion', async () => {
  const recognized = {
    ...sample,
    pattern: 'maximum context length <number>',
    count: 1,
    label: 'Context length exceeded',
    matched: true,
  }
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce(inventoryResponse([sample, recognized], 10, 'older'))
    .mockResolvedValueOnce(
      inventoryResponse(
        [{ ...sample, count: 5, last_seen: sample.last_seen + 1 }],
        25
      )
    )
  const user = userEvent.setup()
  renderRules('[]')
  expect(get).not.toHaveBeenCalled()
  await user.click(screen.getByRole('tab', { name: 'Historical errors' }))
  await screen.findByText(sample.pattern)
  expect(get).toHaveBeenCalledWith(
    '/api/option/error_recognition_rules/inventory',
    expect.objectContaining({ params: { days: 0 } })
  )
  expect(screen.getByRole('status')).not.toHaveTextContent('Scan complete')
  await user.click(
    screen.getByRole('button', { name: 'Scan remaining history' })
  )
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Scanned 35 records')
  )
  expect(screen.getByRole('status')).toHaveTextContent('Scan complete')
  expect(get).toHaveBeenLastCalledWith(
    '/api/option/error_recognition_rules/inventory',
    expect.objectContaining({ params: { days: 0, cursor: 'older' } })
  )
  const row = screen.getByRole('row', { name: /unrecognized upstream failure/ })
  expect(within(row).getByRole('cell', { name: '7' })).toBeVisible()
  await user.click(
    screen.getByRole('switch', { name: 'Unmatched errors only' })
  )
  expect(screen.queryByText(recognized.pattern)).not.toBeInTheDocument()
  await user.click(within(row).getByRole('button', { name: 'Create rule' }))
  expect(screen.getByRole('tab', { name: 'Rules' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('textbox', { name: 'Error pattern' })).toHaveValue(
    sample.suggested_pattern
  )
  expect(
    screen.getByRole('textbox', { name: 'Status codes (optional)' })
  ).toHaveValue('503')
  expect(api.put).not.toHaveBeenCalled()
})

test('pauses scanning after the current batch and resumes without rescanning completed batches', async () => {
  let finishBatch:
    | ((value: ReturnType<typeof inventoryResponse>) => void)
    | undefined
  const pending = new Promise<ReturnType<typeof inventoryResponse>>(
    (resolve) => {
      finishBatch = resolve
    }
  )
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce(inventoryResponse([sample], 10, 'second'))
    .mockImplementationOnce(() => pending)
    .mockResolvedValueOnce(inventoryResponse([], 10))
  const user = userEvent.setup()
  renderRules('[]')
  await user.click(screen.getByRole('tab', { name: 'Historical errors' }))
  await user.click(
    await screen.findByRole('button', { name: 'Scan remaining history' })
  )
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  await user.click(screen.getByRole('button', { name: 'Pause scan' }))
  await act(async () => {
    finishBatch?.(inventoryResponse([], 10, 'third'))
    await pending
  })
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Scanned 20 records')
  )
  expect(get).toHaveBeenCalledTimes(2)
  expect(screen.getByRole('status')).not.toHaveTextContent('Scan complete')
  await user.click(
    screen.getByRole('button', { name: 'Scan remaining history' })
  )
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Scanned 30 records')
  )
  expect(screen.getByRole('status')).toHaveTextContent('Scan complete')
  expect(get).toHaveBeenLastCalledWith(
    '/api/option/error_recognition_rules/inventory',
    expect.objectContaining({ params: { days: 0, cursor: 'third' } })
  )
})

test('retains scanned results after an inventory failure and only marks completion after a successful retry', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce(inventoryResponse([sample], 10, 'retry-cursor'))
    .mockRejectedValueOnce(new Error('Inventory unavailable'))
    .mockResolvedValueOnce(inventoryResponse([], 5))
  const user = userEvent.setup()
  renderRules('[]')
  await user.click(screen.getByRole('tab', { name: 'Historical errors' }))
  await user.click(
    await screen.findByRole('button', { name: 'Scan remaining history' })
  )
  expect(await screen.findByText('Inventory unavailable')).toBeVisible()
  expect(screen.getByText(sample.pattern)).toBeVisible()
  expect(screen.getByRole('status')).toHaveTextContent('Scanned 10 records')
  expect(screen.getByRole('status')).not.toHaveTextContent('Scan complete')
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Scan complete')
  )
  expect(screen.getByRole('status')).toHaveTextContent('Scanned 15 records')
  expect(get).toHaveBeenLastCalledWith(
    '/api/option/error_recognition_rules/inventory',
    expect.objectContaining({ params: { days: 0, cursor: 'retry-cursor' } })
  )
})

test('validates server rule shapes without evaluating RE2 patterns in JavaScript', async () => {
  const translations = createInstance()
  await translations.init({ lng: 'en', resources: {} })
  const re2 = {
    ...rule('word'),
    match: 'regex' as const,
    pattern: '(?i)maximum context length',
  }
  expect(
    parseErrorRecognitionRules(JSON.stringify([re2]), translations.t)
  ).toEqual([re2])
  expect(parseErrorRecognitionRules('[]', translations.t)).toEqual([])
  for (const invalid of [
    { ...rule('bad'), status_codes: [200] },
    { ...rule('bad'), label: 'line\nbreak' },
    { ...rule('bad'), pattern: 'x'.repeat(2049) },
  ]) {
    expect(
      parseErrorRecognitionRules(JSON.stringify([invalid]), translations.t)
    ).toBeNull()
  }
})
