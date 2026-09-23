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
import { describe, expect, test } from 'vitest'

import { buildRequestTraceConversation } from '../../lib/request-trace-conversation'
import type { RequestTraceLeg } from '../../types'
import { RequestTraceConversation } from '../dialogs/request-trace-conversation'

function leg(
  body: unknown,
  overrides: Partial<RequestTraceLeg> = {}
): RequestTraceLeg {
  return {
    seq: 0,
    attempt: 0,
    direction: 'client_request',
    channel_id: 0,
    format: 'openai',
    method: 'POST',
    url: '',
    status: 0,
    headers: null,
    body: JSON.stringify(body),
    body_size: 0,
    truncated: false,
    has_object: false,
    ...overrides,
  }
}

describe('request trace conversation', () => {
  test('joins client history with the final reply without repeating upstream retries', () => {
    const legs = [
      leg(
        { choices: [{ message: { content: 'Final answer' } }] },
        { seq: 5, direction: 'client_response' }
      ),
      leg({
        messages: [
          { role: 'system', content: 'Be concise' },
          { role: 'user', content: 'Question' },
        ],
      }),
      leg(
        {},
        {
          seq: 1,
          direction: 'upstream_response',
          rendered: { content: 'Failed attempt', stream: false },
        }
      ),
      leg(
        { messages: [{ role: 'user', content: 'Mapped question' }] },
        { seq: 2, attempt: 1, direction: 'upstream_request' }
      ),
      leg(
        {},
        {
          seq: 3,
          attempt: 1,
          direction: 'upstream_response',
          rendered: { content: 'Upstream copy', stream: false },
        }
      ),
    ]

    render(<RequestTraceConversation legs={legs} />)

    expect(screen.getByRole('article', { name: 'System' })).toHaveTextContent(
      'Be concise'
    )
    expect(screen.getByRole('article', { name: 'User' })).toHaveTextContent(
      'Question'
    )
    expect(
      screen.getByRole('article', { name: 'Assistant' })
    ).toHaveTextContent('Final answer')
    expect(screen.getAllByRole('article')).toHaveLength(3)
    expect(screen.queryByText('Failed attempt')).not.toBeInTheDocument()
    expect(screen.queryByText('Upstream copy')).not.toBeInTheDocument()
  })

  test('DSH Chat history retains two reasoning and bash tool rounds without treating them as the current reply', () => {
    const sources = buildRequestTraceConversation([
      leg({
        model: 'deepseek-v4-pro',
        messages: [
          { role: 'user', content: 'Check the branch and working tree' },
          {
            role: 'assistant',
            content: '',
            reasoning_content: 'First inspect the current branch',
            tool_calls: [
              {
                id: 'call_branch',
                type: 'function',
                function: {
                  name: 'bash',
                  arguments: '{"command":"git branch --show-current"}',
                },
              },
            ],
          },
          { role: 'tool', tool_call_id: 'call_branch', content: 'main' },
          {
            role: 'assistant',
            content: '',
            reasoning_content: 'Now check for changes',
            tool_calls: [
              {
                id: 'call_status',
                type: 'function',
                function: {
                  name: 'bash',
                  arguments: '{"command":"git status --short"}',
                },
              },
            ],
          },
          { role: 'tool', tool_call_id: 'call_status', content: '(no output)' },
        ],
      }),
      leg(
        {
          choices: [
            {
              index: 0,
              finish_reason: 'stop',
              message: {
                role: 'assistant',
                reasoning_content: 'Both checks have finished',
                content: 'The main branch has a clean working tree',
              },
            },
          ],
        },
        { seq: 1, direction: 'client_response' }
      ),
    ])

    expect(sources[0].messages).toEqual([
      {
        role: 'user',
        parts: [{ type: 'text', text: 'Check the branch and working tree' }],
      },
      {
        role: 'assistant',
        parts: [
          { type: 'reasoning', text: 'First inspect the current branch' },
          { type: 'text', text: '' },
          {
            type: 'tool-call',
            id: 'call_branch',
            name: 'bash',
            value: { command: 'git branch --show-current' },
            linked: true,
            result: { value: 'main' },
          },
        ],
      },
      {
        role: 'tool',
        parts: [
          {
            type: 'tool-result',
            id: 'call_branch',
            name: 'bash',
            value: 'main',
            linked: true,
          },
        ],
      },
      {
        role: 'assistant',
        parts: [
          { type: 'reasoning', text: 'Now check for changes' },
          { type: 'text', text: '' },
          {
            type: 'tool-call',
            id: 'call_status',
            name: 'bash',
            value: { command: 'git status --short' },
            linked: true,
            result: { value: '(no output)' },
          },
        ],
      },
      {
        role: 'tool',
        parts: [
          {
            type: 'tool-result',
            id: 'call_status',
            name: 'bash',
            value: '(no output)',
            linked: true,
          },
        ],
      },
    ])
    expect(sources[1].messages).toEqual([
      {
        role: 'assistant',
        parts: [
          { type: 'reasoning', text: 'Both checks have finished' },
          { type: 'text', text: 'The main branch has a clean working tree' },
        ],
      },
    ])
    expect(sources.every((source) => !source.rawFallback)).toBe(true)
  })

  test.each([
    {
      content: [{ type: 'reasoning_text', text: 'Full recorded reasoning' }],
      summary: [],
      expected: 'Full recorded reasoning',
    },
    {
      content: [{ type: 'reasoning_text', text: 'Full recorded reasoning' }],
      summary: [{ type: 'summary_text', text: 'Short summary' }],
      expected: 'Full recorded reasoning',
    },
    {
      content: [],
      summary: [{ type: 'summary_text', text: 'Summary-only reasoning' }],
      expected: 'Summary-only reasoning',
    },
  ])(
    'Responses shows plaintext reasoning before its summary in requests and replies: $expected',
    ({ content, summary, expected }) => {
      const reasoning = { type: 'reasoning', content, summary }
      const sources = buildRequestTraceConversation([
        leg({ input: [reasoning] }, { format: 'openai_responses' }),
        leg(
          { output: [reasoning] },
          { seq: 1, direction: 'client_response', format: 'openai_responses' }
        ),
      ])

      for (const source of sources) {
        expect(source.messages).toEqual([
          { role: 'assistant', parts: [{ type: 'reasoning', text: expected }] },
        ])
        expect(source.rawFallback).toBe(false)
      }
    }
  )

  test('Responses string input and instructions become distinct system and user turns', () => {
    const sources = buildRequestTraceConversation([
      leg(
        {
          instructions: 'Be concise',
          input: 'Question',
          previous_response_id: 'resp_previous',
        },
        { format: 'openai_responses' }
      ),
    ])

    expect(sources[0].messages).toEqual([
      { role: 'system', parts: [{ type: 'text', text: 'Be concise' }] },
      { role: 'user', parts: [{ type: 'text', text: 'Question' }] },
    ])
    expect(sources[0].referencedHistory).toBe(true)
  })

  test.each([
    {
      format: 'openai',
      body: {
        messages: [
          {
            role: 'assistant',
            tool_calls: [
              {
                id: 'call_1',
                type: 'function',
                function: { name: 'weather', arguments: '{"city":"Paris"}' },
              },
            ],
          },
          { role: 'tool', tool_call_id: 'call_1', content: 'Sunny' },
        ],
      },
    },
    {
      format: 'openai_responses',
      body: {
        input: [
          {
            type: 'function_call',
            call_id: 'call_1',
            name: 'weather',
            arguments: '{"city":"Paris"}',
          },
          { type: 'function_call_output', call_id: 'call_1', output: 'Sunny' },
        ],
      },
    },
    {
      format: 'claude',
      body: {
        messages: [
          {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'call_1',
                name: 'weather',
                input: { city: 'Paris' },
              },
            ],
          },
          {
            role: 'user',
            content: [
              { type: 'tool_result', tool_use_id: 'call_1', content: 'Sunny' },
            ],
          },
        ],
      },
    },
    {
      format: 'gemini',
      body: {
        contents: [
          {
            role: 'model',
            parts: [
              {
                functionCall: {
                  id: 'call_1',
                  name: 'weather',
                  args: { city: 'Paris' },
                },
              },
            ],
          },
          {
            role: 'user',
            parts: [
              {
                functionResponse: {
                  id: 'call_1',
                  name: 'weather',
                  response: 'Sunny',
                },
              },
            ],
          },
        ],
      },
    },
  ])(
    '$format shows matching tool parameters and result in one card',
    async ({ format, body }) => {
      const legs = [leg(body, { format })]
      const sources = buildRequestTraceConversation(legs)
      render(<RequestTraceConversation legs={legs} />)
      const call = screen.getByRole('button', {
        name: /Tool call weather call_1/,
      })
      call.focus()
      await userEvent.keyboard('{Enter}')

      expect(call).toHaveAttribute('aria-expanded', 'true')
      const message = within(screen.getByRole('article', { name: 'Assistant' }))
      expect(message.getByText('Parameters')).toBeInTheDocument()
      expect(message.getByText('Tool result')).toBeInTheDocument()
      expect(message.getByText('Sunny')).toBeInTheDocument()
      expect(
        screen.queryByRole('article', { name: 'Tool' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /Tool result weather/ })
      ).not.toBeInTheDocument()
      expect(screen.queryByText('Completed')).not.toBeInTheDocument()

      expect(sources[0].messages).toEqual([
        {
          role: 'assistant',
          parts: [
            expect.objectContaining({
              type: 'tool-call',
              id: 'call_1',
              name: 'weather',
              value: { city: 'Paris' },
              linked: true,
            }),
          ],
        },
        {
          role: 'tool',
          parts: [
            expect.objectContaining({
              type: 'tool-result',
              id: 'call_1',
              name: 'weather',
              value: 'Sunny',
              linked: true,
            }),
          ],
        },
      ])
    }
  )

  test.each([
    {
      format: 'claude',
      input: {
        system: [{ type: 'text', text: 'Be concise' }],
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'Question' }] },
        ],
      },
      output: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Considering' },
          { type: 'text', text: 'Answer' },
        ],
      },
    },
    {
      format: 'gemini',
      input: {
        system_instruction: { parts: [{ text: 'Be concise' }] },
        contents: [{ role: 'user', parts: [{ text: 'Question' }] }],
      },
      output: {
        candidates: [
          {
            content: {
              role: 'model',
              parts: [
                { thought: true, text: 'Considering' },
                { text: 'Answer' },
              ],
            },
          },
        ],
      },
    },
    {
      format: 'openai_responses',
      input: {
        instructions: 'Be concise',
        input: [
          { role: 'user', content: [{ type: 'input_text', text: 'Question' }] },
        ],
      },
      output: {
        output: [
          {
            type: 'reasoning',
            summary: [{ type: 'summary_text', text: 'Considering' }],
          },
          {
            type: 'message',
            role: 'assistant',
            content: [{ type: 'output_text', text: 'Answer' }],
          },
        ],
      },
    },
  ])(
    '$format preserves system instructions, input text, reasoning and response text',
    ({ format, input, output }) => {
      const sources = buildRequestTraceConversation([
        leg(input, { format }),
        leg(output, { seq: 1, direction: 'client_response', format }),
      ])

      expect(sources[0].messages).toEqual([
        { role: 'system', parts: [{ type: 'text', text: 'Be concise' }] },
        { role: 'user', parts: [{ type: 'text', text: 'Question' }] },
      ])
      expect(sources[1].messages.flatMap((message) => message.parts)).toEqual([
        { type: 'reasoning', text: 'Considering' },
        { type: 'text', text: 'Answer' },
      ])
    }
  )

  test('Gemini defaults omitted roles to user while preserving unknown input shapes', () => {
    const unknownRole = {
      role: 'future-role',
      parts: [{ text: 'Unrecognized role' }],
    }
    const source = buildRequestTraceConversation([
      leg(
        {
          contents: [
            { parts: [{ text: 'Hello' }] },
            unknownRole,
            'Unrecognized content',
          ],
        },
        { format: 'gemini' }
      ),
    ])[0]

    expect(source.messages).toEqual([
      { role: 'user', parts: [{ type: 'text', text: 'Hello' }] },
      { role: 'unknown', parts: [{ type: 'raw', value: unknownRole }] },
      {
        role: 'unknown',
        parts: [{ type: 'raw', value: 'Unrecognized content' }],
      },
    ])
  })

  test('Gemini links an unambiguous name but leaves parallel same-name calls unpaired', () => {
    const source = buildRequestTraceConversation([
      leg(
        {
          contents: [
            {
              role: 'model',
              parts: [{ functionCall: { name: 'weather', args: {} } }],
            },
            {
              role: 'user',
              parts: [
                { functionResponse: { name: 'weather', response: 'Sunny' } },
              ],
            },
            {
              role: 'model',
              parts: [
                { functionCall: { name: 'lookup', args: {} } },
                { functionCall: { name: 'lookup', args: {} } },
              ],
            },
            {
              role: 'user',
              parts: [
                {
                  functionResponse: {
                    name: 'lookup',
                    response: 'Unknown match',
                  },
                },
              ],
            },
          ],
        },
        { format: 'gemini' }
      ),
    ])[0]

    expect(source.messages[1].parts[0]).toMatchObject({
      linked: true,
      name: 'weather',
    })
    expect(source.messages[3].parts[0]).not.toHaveProperty('linked')
  })

  test('truncated input renders complete leading messages without inventing the cut message', () => {
    const history = [
      { role: 'system', content: 'Follow the recorded instructions' },
      { role: 'user', content: 'Inspect braces { [ ] } and escaped "quotes"' },
      {
        role: 'assistant',
        reasoning_content: 'Run the check',
        tool_calls: [
          {
            id: 'call_python',
            type: 'function',
            function: { name: 'python', arguments: '{"code":"print(1)"}' },
          },
        ],
      },
    ]
    const body =
      `{"messages":[${history.map((message) => JSON.stringify(message)).join(',')},` +
      '{"role":"tool","tool_call_id":"call_python","content":"missing' +
      '\n\n... [12345 bytes elided] ...\n\n' +
      ' remainder"},{"role":"user","content":"Unanchored suffix"}],"stream":true}'
    const legs = [leg(null, { body, truncated: true })]
    const source = buildRequestTraceConversation(legs)[0]

    render(<RequestTraceConversation legs={legs} />)

    expect(source.messages).toHaveLength(3)
    expect(source.messages[2].parts).toContainEqual(
      expect.objectContaining({ type: 'tool-call', name: 'python' })
    )
    expect(
      source.messages[2].parts.find((part) => part.type === 'tool-call')
    ).not.toHaveProperty('linked')
    expect(source.messages[1].parts).toEqual([
      { type: 'text', text: 'Inspect braces { [ ] } and escaped "quotes"' },
    ])
    expect(screen.getByRole('article', { name: 'User' })).toHaveTextContent(
      'Inspect braces { [ ] } and escaped'
    )
    expect(
      screen.getByRole('button', { name: /Tool call python call_python/ })
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('article', { name: 'Tool' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Unanchored suffix')).not.toBeInTheDocument()
    expect(
      screen.getByText(
        'This payload was truncated. The conversation may be incomplete.'
      )
    ).toBeInTheDocument()
    expect(
      screen.queryByText(
        'This payload cannot be shown as a conversation. Inspect its raw content in Exchange details.'
      )
    ).not.toBeInTheDocument()
    expect(screen.getByText('Raw payload')).toBeInTheDocument()
  })

  test.each([
    {
      format: 'openai',
      prefix: '"messages":[',
      message: { role: 'user', content: 'Retained message' },
    },
    {
      format: 'claude',
      prefix: '"system":"Keep instructions","messages":[',
      message: {
        role: 'user',
        content: [{ type: 'text', text: 'Retained message' }],
      },
    },
    {
      format: 'openai_responses',
      prefix: '"instructions":"Keep instructions","input":[',
      message: {
        role: 'user',
        content: [{ type: 'input_text', text: 'Retained message' }],
      },
    },
    {
      format: 'openai_responses_compaction',
      prefix: '"input":[',
      message: {
        type: 'function_call_output',
        call_id: 'call_1',
        output: 'Retained message',
      },
    },
    {
      format: 'gemini',
      prefix:
        '"systemInstruction":{"parts":[{"text":"Keep instructions"}]},"contents":[',
      message: { parts: [{ text: 'Retained message' }] },
    },
  ])(
    '$format recovers only complete protocol array items before truncation',
    ({ format, prefix, message }) => {
      const body = `{${prefix}${JSON.stringify(message)},{"content":"cut\n\n... [100 bytes elided] ...\n\ntail"}]}`
      const source = buildRequestTraceConversation([
        leg(null, { format, body, truncated: true }),
      ])[0]

      expect(source.messages.at(-1)?.parts).toContainEqual(
        expect.objectContaining(
          format === 'openai_responses_compaction'
            ? { type: 'tool-result', value: 'Retained message' }
            : { type: 'text', text: 'Retained message' }
        )
      )
      expect(source.rawFallback).toBe(true)
    }
  )

  test('complete history after an omitted unrelated field is recovered from its retained field name', () => {
    const body =
      '{"metadata":{"text":"cut\n\n... [100 bytes elided] ...\n\ntail"},"messages":[{"role":"user","content":"Anchored tail message"}],"stream":true}'
    const source = buildRequestTraceConversation([
      leg(null, { body, truncated: true }),
    ])[0]

    expect(source.messages).toEqual([
      {
        role: 'user',
        parts: [{ type: 'text', text: 'Anchored tail message' }],
      },
    ])
  })

  test.each([
    '{"metadata":{"messages":[{"role":"user","content":"Nested message"}],"text":"cut\n\n... [100 bytes elided] ...\n\ntail"}}',
    '{"messages":[{"role":"user","content":"broken"} {"role":"assistant","content":"Missing comma"},"cut\n\n... [100 bytes elided] ...\n\ntail"]}',
    '{"messages":[{"role":"user","content":"cut\n\n... [100 bytes elided] ...\n\nfirst\n\n... [200 bytes elided] ...\n\nlast"}]}',
    '{"messages":[123\n\n... [100 bytes elided] ...\n\n456]}',
  ])(
    'ambiguous or malformed truncated input does not invent messages',
    (body) => {
      const source = buildRequestTraceConversation([
        leg(null, { body, truncated: true }),
      ])[0]

      expect(source.messages).toHaveLength(0)
      expect(source.rawFallback).toBe(true)
    }
  )

  test('a complete JSON payload marked truncated still renders valid messages and preserves the warning', () => {
    const body = {
      messages: [
        {
          role: 'user',
          content:
            'Literal marker: \n\n... [100 bytes elided] ...\n\n retained',
        },
      ],
    }
    const source = buildRequestTraceConversation([
      leg(body, { truncated: true }),
    ])[0]

    expect(source.messages).toEqual([
      {
        role: 'user',
        parts: [{ type: 'text', text: body.messages[0].content }],
      },
    ])
    expect(source.rawFallback).toBe(true)
  })

  test('truncated client input stays raw while a partial stream reply remains visibly incomplete', () => {
    const legs = [
      leg(null, { body: '{"messages":[truncated]', truncated: true }),
      leg(
        { messages: [{ role: 'user', content: 'Converted input' }] },
        { seq: 1, direction: 'upstream_request' }
      ),
      leg(null, {
        seq: 2,
        direction: 'client_response',
        body: 'data: {"partial":true}',
        truncated: true,
        rendered: { content: 'Partial answer', stream: true },
      }),
    ]

    const sources = buildRequestTraceConversation(legs)
    render(<RequestTraceConversation legs={legs} />)

    expect(sources[0].rawFallback).toBe(true)
    expect(sources[0].messages).toHaveLength(0)
    expect(screen.queryByText('Converted input')).not.toBeInTheDocument()
    expect(
      screen.getAllByText(
        'This payload was truncated. The conversation may be incomplete.'
      )
    ).toHaveLength(2)
    expect(screen.getByText('Partial answer')).toBeInTheDocument()
    expect(screen.getAllByText('Raw payload')).toHaveLength(2)
  })

  test('unknown roles and multimodal blocks preserve their original content', () => {
    const image = {
      type: 'image_url',
      image_url: { url: 'https://example.com/input.png' },
    }
    const unknown = { role: 'future-role', content: { opaque: 1 } }
    const sources = buildRequestTraceConversation([
      leg({
        messages: [
          {
            role: 'user',
            content: [{ type: 'text', text: 'Describe' }, image],
          },
          unknown,
        ],
      }),
    ])

    expect(sources[0].messages).toEqual([
      {
        role: 'user',
        parts: [
          { type: 'text', text: 'Describe' },
          { type: 'raw', value: image },
        ],
      },
      { role: 'unknown', parts: [{ type: 'raw', value: unknown }] },
    ])
  })

  test('unrecorded client payloads fall back only to the final upstream attempt', () => {
    const sources = buildRequestTraceConversation([
      leg(
        { messages: [{ role: 'user', content: 'First try' }] },
        { direction: 'upstream_request' }
      ),
      leg(
        {},
        {
          seq: 1,
          direction: 'upstream_response',
          rendered: { content: 'Earlier result', stream: false },
        }
      ),
      leg(
        { messages: [{ role: 'user', content: 'Final try' }] },
        { seq: 2, attempt: 1, direction: 'upstream_request' }
      ),
    ])

    expect(sources).toHaveLength(1)
    expect(sources[0].messages[0].parts).toEqual([
      { type: 'text', text: 'Final try' },
    ])
  })

  test('recorded calls expand with the keyboard without claiming a completed execution', async () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(
            {},
            {
              direction: 'client_response',
              rendered: {
                stream: true,
                tool_calls: [
                  {
                    id: 'call_1',
                    name: 'weather',
                    arguments: '{"city":"Paris"}',
                  },
                ],
              },
            }
          ),
        ]}
      />
    )

    const trigger = screen.getByRole('button', {
      name: /Tool call weather call_1/,
    })
    trigger.focus()
    await userEvent.keyboard('{Enter}')

    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(
      within(screen.getByRole('article', { name: 'Assistant' })).getByText(
        'Parameters'
      )
    ).toBeInTheDocument()
    expect(screen.queryByText('Completed')).not.toBeInTheDocument()
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
    expect(screen.queryByText('Result recorded')).not.toBeInTheDocument()
  })

  test.each([0, false, '', null, { value: 'Structured result' }])(
    'recorded result %j stays visible inside its matching tool card',
    async (value) => {
      const legs = [
        leg(
          {
            input: [
              {
                type: 'function_call',
                call_id: 'call_1',
                name: 'lookup',
                arguments: '{}',
              },
              {
                type: 'function_call_output',
                call_id: 'call_1',
                output: value,
              },
            ],
          },
          { format: 'openai_responses' }
        ),
      ]
      const source = buildRequestTraceConversation(legs)[0]
      render(<RequestTraceConversation legs={legs} />)
      await userEvent.click(
        screen.getByRole('button', { name: /Tool call lookup call_1/ })
      )

      expect(source.messages[0].parts[0]).toMatchObject({ result: { value } })
      expect(screen.getByText('Tool result')).toBeInTheDocument()
      expect(screen.getByText('Result recorded')).toBeInTheDocument()
      expect(
        screen.queryByRole('article', { name: 'Tool' })
      ).not.toBeInTheDocument()
    }
  )

  test('a failed tool result is grouped with its input without hiding surrounding user text', async () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(
            {
              messages: [
                {
                  role: 'assistant',
                  content: [
                    {
                      type: 'tool_use',
                      id: 'call_1',
                      name: 'lookup',
                      input: { id: 7 },
                    },
                  ],
                },
                {
                  role: 'user',
                  content: [
                    { type: 'text', text: 'Before the tool result' },
                    {
                      type: 'tool_result',
                      tool_use_id: 'call_1',
                      content: 'Lookup failed',
                      is_error: true,
                    },
                    { type: 'text', text: 'After the tool result' },
                  ],
                },
              ],
            },
            { format: 'claude' }
          ),
        ]}
      />
    )
    const message = within(screen.getByRole('article', { name: 'Assistant' }))
    expect(message.getByText('Error')).toBeInTheDocument()
    await userEvent.click(
      message.getByRole('button', { name: /Tool call lookup call_1/ })
    )

    expect(message.getByText('Lookup failed')).toBeInTheDocument()
    expect(message.getByText('Parameters')).toBeInTheDocument()
    expect(screen.getByText('Before the tool result')).toBeInTheDocument()
    expect(screen.getByText('After the tool result')).toBeInTheDocument()
    expect(
      screen.queryByRole('article', { name: 'Tool' })
    ).not.toBeInTheDocument()
  })

  test.each([
    [
      {
        type: 'function_call',
        call_id: 'same',
        name: 'lookup',
        arguments: '{}',
      },
      {
        type: 'function_call',
        call_id: 'same',
        name: 'lookup',
        arguments: '{}',
      },
      {
        type: 'function_call_output',
        call_id: 'same',
        output: 'Ambiguous result',
      },
    ],
    [
      {
        type: 'function_call',
        call_id: 'same',
        name: 'lookup',
        arguments: '{}',
      },
      {
        type: 'function_call_output',
        call_id: 'same',
        output: 'First ambiguous result',
      },
      {
        type: 'function_call_output',
        call_id: 'same',
        output: 'Second ambiguous result',
      },
    ],
    [
      {
        type: 'function_call_output',
        call_id: 'same',
        output: 'Result recorded before a call',
      },
      {
        type: 'function_call',
        call_id: 'same',
        name: 'lookup',
        arguments: '{}',
      },
    ],
  ])('ambiguous or out-of-order tool data stays separate: %j', (...input) => {
    const legs = [leg({ input }, { format: 'openai_responses' })]
    const source = buildRequestTraceConversation(legs)[0]
    render(<RequestTraceConversation legs={legs} />)

    for (const message of source.messages) {
      for (const part of message.parts) {
        expect(part).not.toHaveProperty('result')
        expect(part).not.toHaveProperty('linked', true)
      }
    }
    expect(
      screen.getAllByRole('article', { name: 'Tool' }).length
    ).toBeGreaterThan(0)
    expect(screen.queryByText('Result recorded')).not.toBeInTheDocument()
  })

  test('Gemini calls and results with conflicting recorded names stay independently visible', () => {
    const legs = [
      leg(
        {
          contents: [
            {
              role: 'model',
              parts: [
                { functionCall: { id: 'call_1', name: 'lookup', args: {} } },
              ],
            },
            {
              role: 'user',
              parts: [
                {
                  functionResponse: {
                    id: 'call_1',
                    name: 'weather',
                    response: 'Sunny',
                  },
                },
              ],
            },
          ],
        },
        { format: 'gemini' }
      ),
    ]
    const source = buildRequestTraceConversation(legs)[0]
    render(<RequestTraceConversation legs={legs} />)

    expect(source.messages[0].parts[0]).not.toHaveProperty('result')
    expect(
      screen.getByRole('button', { name: /Tool call lookup call_1/ })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Tool result weather call_1/ })
    ).toBeInTheDocument()
    expect(screen.queryByText('Result recorded')).not.toBeInTheDocument()
  })

  test('a grouped Gemini card retains the name recorded only on the matching result', () => {
    const legs = [
      leg(
        {
          contents: [
            {
              role: 'model',
              parts: [{ functionCall: { id: 'call_1', args: {} } }],
            },
            {
              role: 'user',
              parts: [
                {
                  functionResponse: {
                    id: 'call_1',
                    name: 'weather',
                    response: 'Sunny',
                  },
                },
              ],
            },
          ],
        },
        { format: 'gemini' }
      ),
    ]
    render(<RequestTraceConversation legs={legs} />)

    expect(
      screen.getByRole('button', { name: /Tool call weather call_1/ })
    ).toBeInTheDocument()
    expect(screen.queryByText('Unnamed tool')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('article', { name: 'Tool' })
    ).not.toBeInTheDocument()
  })

  test.each([
    { callName: undefined, firstName: 'weather', secondName: 'other' },
    { callName: 'weather', firstName: undefined, secondName: 'other' },
  ])(
    'duplicate results restore original tool names after undoing an ambiguous pair: $callName / $firstName',
    ({ callName, firstName, secondName }) => {
      const legs = [
        leg(
          {
            contents: [
              {
                role: 'model',
                parts: [
                  { functionCall: { id: 'call_1', name: callName, args: {} } },
                ],
              },
              {
                role: 'user',
                parts: [
                  {
                    functionResponse: {
                      id: 'call_1',
                      name: firstName,
                      response: 'First result',
                    },
                  },
                ],
              },
              {
                role: 'user',
                parts: [
                  {
                    functionResponse: {
                      id: 'call_1',
                      name: secondName,
                      response: 'Second result',
                    },
                  },
                ],
              },
            ],
          },
          { format: 'gemini' }
        ),
      ]
      const source = buildRequestTraceConversation(legs)[0]

      expect(source.messages[0].parts[0]).toMatchObject({ name: callName })
      expect(source.messages[0].parts[0]).not.toHaveProperty('result')
      const firstResult = source.messages[1].parts[0]
      expect('name' in firstResult ? firstResult.name : undefined).toBe(
        firstName
      )
      expect(source.messages[1].parts[0]).not.toHaveProperty('linked', true)
      expect(source.messages[2].parts[0]).toMatchObject({ name: secondName })
    }
  )

  test('copying a grouped tool message includes both parameters and its recorded result', async () => {
    const user = userEvent.setup()
    render(
      <RequestTraceConversation
        legs={[
          leg(
            {
              input: [
                {
                  type: 'function_call',
                  call_id: 'call_1',
                  name: 'lookup',
                  arguments: '{"id":7}',
                },
                {
                  type: 'function_call_output',
                  call_id: 'call_1',
                  output: { found: false },
                },
              ],
            },
            { format: 'openai_responses' }
          ),
        ]}
      />
    )
    const message = within(screen.getByRole('article', { name: 'Assistant' }))
    await user.click(message.getByRole('button', { name: 'Copy message' }))

    expect(JSON.parse(await navigator.clipboard.readText())).toEqual({
      id: 'call_1',
      name: 'lookup',
      'tool-call': { id: 7 },
      'tool-result': { found: false },
    })
  })

  test('a result after truncated input is not grouped with a call across the missing history', () => {
    const legs = [
      leg(
        {
          input: [
            {
              type: 'function_call',
              call_id: 'call_1',
              name: 'lookup',
              arguments: '{}',
            },
          ],
        },
        { format: 'openai_responses', truncated: true }
      ),
      leg(
        {
          output: [
            {
              type: 'function_call_output',
              call_id: 'call_1',
              output: 'After missing history',
            },
          ],
        },
        { seq: 1, direction: 'client_response', format: 'openai_responses' }
      ),
    ]
    const sources = buildRequestTraceConversation(legs)
    render(<RequestTraceConversation legs={legs} />)

    expect(sources[0].messages[0].parts[0]).not.toHaveProperty('result')
    expect(sources[1].messages[0].parts[0]).not.toHaveProperty('linked', true)
    expect(screen.getByRole('article', { name: 'Tool' })).toBeInTheDocument()
    expect(screen.queryByText('Result recorded')).not.toBeInTheDocument()
  })

  test('a long matched result remains under the tool card without hiding its header', async () => {
    const longResult = `Large tool result ${'output '.repeat(1_300)}`
    render(
      <RequestTraceConversation
        legs={[
          leg({
            messages: [
              {
                role: 'assistant',
                tool_calls: [
                  {
                    id: 'call_1',
                    type: 'function',
                    function: { name: 'lookup', arguments: '{}' },
                  },
                ],
              },
              { role: 'tool', tool_call_id: 'call_1', content: longResult },
            ],
          }),
        ]}
      />
    )
    const call = screen.getByRole('button', { name: /Tool call lookup call_1/ })
    expect(call).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(call)

    expect(call).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('Tool result')).toBeInTheDocument()
    expect(
      screen.getByRole('article', { name: 'Assistant' })
    ).toHaveTextContent('Large tool result')
  })

  test('unsupported binary and error payloads expose a raw fallback instead of an empty assistant turn', () => {
    const sources = buildRequestTraceConversation([
      leg(null, { format: 'openai_audio', body: '', has_object: true }),
      leg(
        { error: { message: 'Quota exceeded' } },
        { seq: 1, direction: 'client_response', status: 429 }
      ),
    ])

    expect(sources.every((source) => source.rawFallback)).toBe(true)
    expect(sources.flatMap((source) => source.messages)).toHaveLength(0)
  })

  test('long messages start collapsed and expand into a bounded scroll area', async () => {
    const longContent = `Large captured message ${'context '.repeat(1_200)}`
    render(
      <RequestTraceConversation
        legs={[leg({ messages: [{ role: 'user', content: longContent }] })]}
      />
    )

    const message = within(screen.getByRole('article', { name: 'User' }))
    const expand = message.getByRole('button', { name: 'Expand' })
    expect(expand).toHaveAttribute('aria-expanded', 'false')
    expect(message.queryByText(longContent)).not.toBeInTheDocument()

    await userEvent.click(expand)

    const collapse = message.getByRole('button', { name: 'Collapse' })
    expect(collapse).toHaveAttribute('aria-expanded', 'true')
    const panel = document.querySelector(
      `[id="${collapse.getAttribute('aria-controls')}"]`
    )
    expect(panel).toHaveTextContent('Large captured message')
    expect(panel).toHaveClass('overflow-auto', 'max-h-[32rem]')
  })

  test('reasoning uses a single-line collapsed label and opens with the keyboard', async () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(
            {},
            {
              direction: 'client_response',
              rendered: {
                reasoning: 'Recorded reasoning',
                content: 'Answer',
                stream: true,
              },
            }
          ),
        ]}
      />
    )

    const trigger = screen.getByRole('button', { name: 'Reasoning' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveClass('inline-flex', 'whitespace-nowrap')
    expect(screen.queryByText('Recorded reasoning')).not.toBeInTheDocument()

    trigger.focus()
    await userEvent.keyboard('{Enter}')

    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('Recorded reasoning')).toBeInTheDocument()
  })

  test('multiple response choices stay separate with access to the full raw payload', () => {
    const sources = buildRequestTraceConversation([
      leg(
        {
          choices: [
            { index: 1, message: { content: 'Alternative answer' } },
            { index: 0, message: { content: 'Primary answer' } },
          ],
        },
        { direction: 'client_response' }
      ),
    ])

    expect(sources[0].hasAlternatives).toBe(true)
    expect(sources[0].rawFallback).toBe(true)
    expect(sources[0].messages).toEqual([
      { role: 'assistant', parts: [{ type: 'text', text: 'Primary answer' }] },
    ])
  })

  test.each([
    {
      format: 'openai',
      body: 'data: {"choices":[{"index":0,"delta":{"content":"First"}},{"index":1,"delta":{"content":"Second"}}]}\n\ndata: [DONE]',
    },
    {
      format: 'gemini',
      body: 'data: {"candidates":[{"index":0,"content":{"parts":[{"text":"First"}]}}]}\n\ndata: {"candidates":[{"index":1,"content":{"parts":[{"text":"Second"}]}}]}',
    },
  ])(
    '$format streamed alternatives expose the primary-only notice and full raw payload',
    ({ format, body }) => {
      const legs = [
        leg(null, {
          format,
          direction: 'client_response',
          body,
          rendered: { content: 'First', stream: true },
        }),
      ]
      const source = buildRequestTraceConversation(legs)[0]
      render(<RequestTraceConversation legs={legs} />)

      expect(source.hasAlternatives).toBe(true)
      expect(source.rawFallback).toBe(true)
      expect(
        screen.getByText(
          'Only the first response alternative is shown. The raw payload contains all alternatives.'
        )
      ).toBeInTheDocument()
      expect(screen.getByText('Raw payload')).toBeInTheDocument()
      expect(
        screen.getByRole('article', { name: 'Assistant' })
      ).toHaveTextContent('First')
    }
  )
})
