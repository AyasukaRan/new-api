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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test } from 'vitest'

import { formatTimestampToDate } from '@/lib/format'

import { usageLogSchema } from '../../data/schema'
import type { LogOtherData } from '../../types'
import {
  LogRequestIdentifiers,
  LogRequestInfoCards,
} from '../log-request-info-cards'

const log = usageLogSchema.parse({
  id: 104,
  user_id: 73,
  created_at: 1795486871,
  type: 2,
  content: '',
  username: 'account-73',
  token_name: 'user-key',
  token_id: 92,
  model_name: 'public-model',
  channel: 12,
  channel_name: 'private-route',
  prompt_tokens: 2,
  completion_tokens: 5,
  is_stream: true,
  ip: '192.0.2.111',
  request_id: 'local-104',
  upstream_request_id: 'remote-75ad03',
})
const other: LogOtherData = {
  input_tokens_total: 15,
  duration_ms: 680,
  frt: 135,
  request_path: '/v1/responses',
  response_transport: 'sse',
  reasoning_effort: 'high',
  response_model: {
    requested_model: 'requested-alias',
    upstream_model: 'upstream-model',
    returned_model: 'returned-model',
  },
  stream_status: { response_status: 'completed', status: 'ok' },
  client_tool: 'example-client',
  admin_info: { user_agent: 'example-private-agent', use_channel: [8, 12] },
}

test('organizes the recorded request and copies complete identifiers and timestamps', async () => {
  const user = userEvent.setup()
  render(
    <>
      <LogRequestInfoCards log={log} other={other} isAdmin />
      <LogRequestIdentifiers log={log} isAdmin />
    </>
  )
  expect(
    screen.getAllByRole('heading').map((heading) => heading.textContent)
  ).toEqual([
    'Request information',
    'Transport and usage',
    'Model and routing',
    'Client and upstream',
    'Request identifiers',
  ])
  const timing = within(
    screen.getByRole('region', { name: 'Transport and usage' })
  )
  expect(timing.getByText('135ms')).toBeVisible()
  expect(timing.getByText('680ms')).toBeVisible()
  expect(timing.getByText('20')).toBeVisible()
  expect(timing.getByText('SSE')).toBeVisible()
  expect(screen.queryByText('Status Code')).not.toBeInTheDocument()
  expect(screen.getByText('8 → 12')).toBeVisible()
  expect(screen.getByText('upstream-model')).toBeVisible()
  expect(screen.getByText('example-private-agent')).toBeVisible()
  await user.click(
    screen.getByRole('button', {
      name: 'Copy to clipboard · Upstream Request ID',
    })
  )
  expect(await navigator.clipboard.readText()).toBe(log.upstream_request_id)
  await user.click(
    screen.getByRole('button', { name: 'Copy to clipboard · Time' })
  )
  expect(await navigator.clipboard.readText()).toBe(
    formatTimestampToDate(log.created_at)
  )
})

test('personal overview omits administrator-only routing, client metadata and identifiers', () => {
  render(
    <>
      <LogRequestInfoCards log={log} other={other} isAdmin={false} />
      <LogRequestIdentifiers log={log} isAdmin={false} />
    </>
  )
  for (const text of [
    'account-73',
    'private-route',
    'upstream-model',
    'returned-model',
    'requested-alias',
    'example-private-agent',
    'example-client',
    '8 → 12',
    '/v1/responses',
    'remote-75ad03',
    'User ID',
    'Token ID',
  ]) {
    expect(screen.queryByText(text)).not.toBeInTheDocument()
  }
  for (const text of ['user-key', 'public-model', 'local-104', '192.0.2.111']) {
    expect(screen.getByText(text)).toBeVisible()
  }
  expect(
    screen.queryByRole('button', {
      name: 'Copy to clipboard · Upstream Request ID',
    })
  ).not.toBeInTheDocument()
})

test('shows the recorded failure code and omits measurements missing from older requests', () => {
  render(
    <LogRequestInfoCards
      log={{
        ...log,
        type: 5,
        content: 'status_code=503, temporarily unavailable',
        prompt_tokens: 0,
        completion_tokens: 0,
      }}
      other={null}
      isAdmin
    />
  )
  expect(screen.getByText('503')).toBeVisible()
  expect(
    screen.queryByText('temporarily unavailable', { exact: false })
  ).not.toBeInTheDocument()
  for (const text of [
    'First token',
    'Total Tokens',
    'Duration',
    'Upstream Model',
    'SSE',
  ]) {
    expect(screen.queryByText(text)).not.toBeInTheDocument()
  }
})
