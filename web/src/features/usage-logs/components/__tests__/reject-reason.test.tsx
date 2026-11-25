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
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'
import { RequestFailureSummary } from '../request-failure-summary'

const queryClients: QueryClient[] = []

function makeLog(other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 5,
    content: 'request rejected',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(isAdmin: boolean, extraOther: LogOtherData = {}): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        initialView='technical'
        log={makeLog({
          admin_info: {
            reject_reason: 'blocked by channel policy',
          },
          ...extraOther,
        })}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
})

describe('usage log reject reason', () => {
  test('shows the nested admin reject reason to admins', () => {
    renderDetails(true)

    expect(screen.getByText('Reject Reason')).toBeInTheDocument()
    expect(screen.getByText('blocked by channel policy')).toBeInTheDocument()
  })

  test('hides the nested admin reject reason from non-admin users', () => {
    renderDetails(false)

    expect(screen.queryByText('Reject Reason')).toBeNull()
    expect(screen.queryByText('blocked by channel policy')).toBeNull()
  })
})

test.each([false, true])(
  'shows a safe failed-stream reason and why retry stopped for admin=%s',
  (isAdmin) => {
    renderDetails(isAdmin, {
      stream_status: {
        status: 'error',
        end_reason: 'upstream_error',
        end_error: 'Upstream temporarily unavailable',
      },
      retry_decision: { action: 'stop', reason: 'response_already_started' },
    })
    expect(screen.getByText('Failure reason')).toBeVisible()
    expect(screen.getByText('Upstream temporarily unavailable')).toBeVisible()
    expect(screen.getByText('Why retry stopped')).toBeVisible()
    expect(
      screen.getByText(
        'Response content has already been sent; retrying could duplicate output'
      )
    ).toBeVisible()
  }
)

test('shows a planned same-key retry separately from its upstream failure', () => {
  renderDetails(false, {
    retry_decision: {
      action: 'retry_same_key',
      reason: 'same_key_transient_retry',
    },
  })
  expect(screen.getByText('Retry decision')).toBeVisible()
  expect(
    screen.getByText('Retry the same key once after a temporary upstream error')
  ).toBeVisible()
  expect(screen.queryByText('Why retry stopped')).not.toBeInTheDocument()
})

test('unknown retry reasons are not exposed as raw diagnostic strings', () => {
  renderDetails(false, {
    retry_decision: {
      action: 'stop',
      reason: 'unrecognized-upstream-secret-identifier',
    },
  })
  expect(screen.getByText('Retry decision unavailable')).toBeVisible()
  expect(
    screen.queryByText('unrecognized-upstream-secret-identifier')
  ).not.toBeInTheDocument()
})

test('a successfully completed response with soft diagnostics is not presented as a failed request', () => {
  render(
    <RequestFailureSummary
      metadata={{
        stream_status: {
          status: 'error',
          response_status: 'completed',
          end_reason: 'done',
          errors: ['A recoverable diagnostic'],
          error_count: 1,
        },
      }}
    />
  )
  expect(screen.queryByText('Request failed')).not.toBeInTheDocument()
})

test('legacy failures without retry or timing metadata show no invented retry reason or first-token value', () => {
  render(
    <RequestFailureSummary
      error='Upstream temporarily unavailable'
      showFirstResponse
    />
  )
  expect(screen.getByText('Not recorded')).toBeVisible()
  expect(screen.queryByText('First token')).not.toBeInTheDocument()
  expect(screen.queryByText('Why retry stopped')).not.toBeInTheDocument()
})

test('context overflow details keep the complete original error and extracted token limits', () => {
  const error =
    "status_code=400, This model's maximum context length is 1048576 tokens. However, you requested 1770481 tokens (1770481 in the messages, 0 in the completion). Please reduce the length of the messages or completion. (request_id: historical-request)"
  render(<RequestFailureSummary error={error} />)

  expect(screen.getByText('Context length exceeded')).toBeVisible()
  expect(screen.getByText('Context limit')).toBeVisible()
  expect(screen.getByText('1,048,576')).toBeVisible()
  expect(screen.getByText('Requested tokens')).toBeVisible()
  expect(screen.getByText('1,770,481')).toBeVisible()
  expect(screen.getByText('Original error')).toBeVisible()
  expect(screen.getByText(error)).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Copy to clipboard' })
  ).toBeVisible()
})
