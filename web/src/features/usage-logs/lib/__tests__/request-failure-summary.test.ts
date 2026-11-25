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
import { describe, expect, test } from 'vitest'

import { isFailedStreamResponse } from '../format'
import { summarizeRequestFailure } from '../request-failure-summary'

describe('request failure summaries', () => {
  test('an incomplete model response fails even when transport ended normally', () => {
    expect(
      isFailedStreamResponse({ status: 'ok', response_status: 'incomplete' })
    ).toBe(true)
    expect(
      isFailedStreamResponse({
        status: 'error',
        response_status: 'completed',
        end_error: 'recoverable diagnostic',
      })
    ).toBe(false)
  })
  test('context overflow preserves the upstream token limits', () => {
    expect(
      summarizeRequestFailure(
        "status_code=400, This model's maximum context length is 1048576 tokens. However, you requested 1770481 tokens (1770481 in the messages, 0 in the completion). Please reduce the length of the messages or completion. (request_id: historical-request)"
      )
    ).toEqual({
      label: 'Context length exceeded',
      statusCode: 400,
      contextLimit: 1048576,
      requestedTokens: 1770481,
    })
  })

  test.each([
    ['status_code=400, invalid temperature', 'Invalid request'],
    ['context_length_exceeded', 'Context length exceeded'],
    ['输入上下文长度超过限制', 'Context length exceeded'],
    [
      'status_code=401, Authentication Fails, Your api key: ****d9f9 is invalid',
      'Upstream authentication failed',
    ],
    ['status_code=429, insufficient_quota', 'Insufficient upstream balance'],
    ['status_code=429, Too Many Requests', 'Rate limited'],
    [
      'status_code=503, <html><title>503 Service Temporarily Unavailable</title></html>',
      'Upstream unavailable',
    ],
    ['Upstream temporarily unavailable', 'Upstream unavailable'],
    [
      'status_code=456, <html><title>Security Verification</title></html>',
      'Upstream security challenge',
    ],
    [
      'status_code=400, tool_use ids were found without tool_result blocks',
      'Tool call mismatch',
    ],
    ['status_code=500, context deadline exceeded', 'Upstream timed out'],
    ['dial tcp: connection refused', 'Upstream connection failed'],
    ['status_code=404, model_not_found', 'Model unavailable'],
    ['status_code=400, content_policy_violation', 'Content blocked'],
    ['status_code=500, unexpected upstream failure', 'Upstream error'],
    ['unexpected EOF', 'Stream interrupted'],
    ['status_code=400, completion limit must be positive', 'Invalid request'],
    [
      'status_code=400, Thinking mode does not support this tool_choice',
      'Unsupported tool choice',
    ],
    [
      'status_code=400, reasoning_text in thinking mode must be passed back',
      'Missing reasoning content',
    ],
    ['status_code=400, Invalid schema for function', 'Invalid tool definition'],
    [
      'status_code=400, response_format unavailable',
      'Unsupported response format',
    ],
    [
      'status_code=503, No available accounts',
      'No available upstream accounts',
    ],
    ['status_code=500, Upstream access forbidden', 'Upstream access denied'],
    ['status_code=403, insufficient balance', 'Insufficient upstream balance'],
    [
      'status_code=456, bad response status code 456',
      'Upstream request rejected',
    ],
    [
      'status_code=422, messages[324] missing field tool_call_id',
      'Tool call mismatch',
    ],
    [
      'status_code=403, Insufficient account balance',
      'Insufficient upstream balance',
    ],
    ['status_code=502, Upstream access forbidden', 'Upstream access denied'],
    [
      'status_code=500, upstream stream disconnected: connection reset by peer',
      'Stream interrupted',
    ],
    ['status_code=400, Content Exists Risk', 'Content blocked'],
    [
      'status_code=400, Invalid `signature` in `thinking` block',
      'Thinking replay mismatch',
    ],
    [
      'status_code=400, input[4].content[2]: Unsupported image_url format',
      'Unsupported image format',
    ],
    ['status_code=404, failed to fetch URL', 'Resource fetch failed'],
    ['unknown reason', 'Request failed'],
    ['', 'Request failed'],
  ])('summarizes %s without exposing the original', (error, label) => {
    expect(summarizeRequestFailure(error).label).toBe(label)
  })

  test('uses source message keys regardless of the viewer language', () => {
    expect(
      summarizeRequestFailure('translated reason', {
        content_parts: [{ key: 'context_length_exceeded' }],
      }).label
    ).toBe('Context length exceeded')
  })

  test('stream end metadata identifies a client disconnect', () => {
    expect(
      summarizeRequestFailure('context canceled', {
        stream_status: { status: 'error', end_reason: 'client_gone' },
      }).label
    ).toBe('Client disconnected')
  })

  test('uses server classification and does not revive disabled rules', () => {
    expect(
      summarizeRequestFailure('context_length_exceeded', {
        error_summary: { label: 'Request failed', matched: false },
      }).label
    ).toBe('Request failed')
    expect(
      summarizeRequestFailure('some original error', {
        error_summary: {
          label: 'Operator label',
          matched: true,
          rule_id: 'custom-1',
        },
      }).label
    ).toBe('Operator label')
  })

  test('recognizes public metadata when legacy content is missing', () => {
    expect(summarizeRequestFailure(undefined, { status_code: 429 })).toEqual({
      label: 'Rate limited',
      statusCode: 429,
    })
  })

  test('does not interpret private request headers or prompts as errors', () => {
    expect(
      summarizeRequestFailure(undefined, {
        admin_info: { request_body: 'context_length_exceeded' },
      })
    ).toEqual({ label: 'Request failed' })
  })
})
