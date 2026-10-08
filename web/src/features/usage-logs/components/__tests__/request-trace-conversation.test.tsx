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
import {
  RequestTraceRawLeg,
  RequestTraceRenderedLeg,
} from '../dialogs/request-trace-leg'

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
  test.each(['\n', '\r\n'])(
    'preserves recorded request line breaks and spacing with %j separators',
    async (separator) => {
      const user = userEvent.setup()
      const content = [
        'Please parse the test information:',
        '',
        '**Project**: Public',
        'appid: sample-app',
        'Language: en_us',
        'Tickets: 1001,  1002',
      ].join(separator)
      render(
        <RequestTraceConversation
          legs={[leg({ messages: [{ role: 'user', content }] })]}
        />
      )

      const message = within(screen.getByRole('article', { name: 'User' }))
      const paragraph = message.getByText(/appid: sample-app/)
      expect(paragraph.textContent).toBe(
        'Project: Public\nappid: sample-app\nLanguage: en_us\nTickets: 1001,  1002'
      )
      // The trace preserves text-node whitespace, including Markdown soft breaks.
      expect(paragraph.closest('.whitespace-pre-wrap')).not.toBeNull()
      expect(message.getByText('Project').tagName).toBe('STRONG')
      await user.click(message.getByRole('button', { name: 'Copy message' }))
      expect(await navigator.clipboard.readText()).toBe(content)
    }
  )

  test.each([
    'Result:\n{"rows":[{"text":"literal **stars** &amp;"}]}\nDone.',
    'Result:\n\n{\n  "rows": [\n\n    {"text":"literal **stars** &amp;"}\n  ]\n}\n\nDone.',
    '```json title="result.json"\n{"rows":[{"text":"literal **stars** &amp;"}]}\n```',
    '```application/json\n{"rows":[{"text":"literal **stars** &amp;"}]}\n```',
    '> Result:\n> {"rows":[{"text":"literal **stars** &amp;"}]}\n> Done.',
  ])(
    'recognizes JSON inside prose and annotated fences without rewriting values: %s',
    async (content) => {
      const user = userEvent.setup()
      render(
        <RequestTraceConversation
          legs={[
            leg(null, {
              direction: 'client_response',
              rendered: { content, stream: false },
            }),
          ]}
        />
      )

      expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'Expand 0' }))
      expect(screen.getByText('"literal **stars** &amp;"')).toBeInTheDocument()
      if (content.includes('Result:')) {
        expect(screen.getByText('Result:')).toBeInTheDocument()
        expect(screen.getByText('Done.')).toBeInTheDocument()
      }
      await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
      expect(JSON.parse(await navigator.clipboard.readText())).toEqual({
        rows: [{ text: 'literal **stars** &amp;' }],
      })
    }
  )

  test.each([
    '[\n  {"used_tag": "first\nsecond", "result": "normal"}\n]',
    '{"rows":[{"id":1}],"unfinished":',
  ])(
    'preserves malformed JSON replies as original code instead of interpreting Markdown: %s',
    async (content) => {
      const user = userEvent.setup()
      render(
        <RequestTraceConversation
          legs={[
            leg(null, {
              direction: 'client_response',
              rendered: { content, stream: false },
            }),
          ]}
        />
      )

      const raw = screen.getByRole('textbox', { name: 'JSON' })
      expect(raw).toHaveTextContent(content.startsWith('[') ? '[' : '{')
      expect(raw).toHaveTextContent(
        content.includes('used_tag') ? '"used_tag"' : '"rows"'
      )
      expect(raw).not.toHaveTextContent('“')
      expect(
        screen.queryByRole('list', { name: 'JSON tree' })
      ).not.toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
      expect(await navigator.clipboard.readText()).toBe(content)
    }
  )

  test('keeps a complete leading JSON document and its following Markdown distinct', () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(null, {
            direction: 'client_response',
            rendered: { content: '{"rows":[1]}\n\n**Done**', stream: false },
          }),
        ]}
      />
    )

    expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
    expect(screen.getByText('Done').tagName).toBe('STRONG')
  })

  test('keeps a leading Markdown link as a link instead of treating it as broken JSON', () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(null, {
            direction: 'client_response',
            rendered: { content: '[1](https://example.com)', stream: false },
          }),
        ]}
      />
    )

    expect(screen.getByRole('link', { name: '1' })).toHaveAttribute(
      'href',
      'https://example.com'
    )
    expect(
      screen.queryByRole('textbox', { name: 'JSON' })
    ).not.toBeInTheDocument()
  })

  test('separate JSON blocks keep surrounding Markdown and original numeric tokens', async () => {
    const user = userEvent.setup()
    const first = '{"rows":[1],"id":9007199254740993}'
    const second = '{"rows":[2]}'
    const content = `**First**\n${first}\n\nBetween outputs.\n\n${second}\n\n**Done**`
    render(
      <RequestTraceConversation
        legs={[
          leg(null, {
            direction: 'client_response',
            rendered: { content, stream: false },
          }),
        ]}
      />
    )

    expect(screen.getAllByRole('button', { name: /rows/ })).toHaveLength(2)
    expect(screen.getByText('First').tagName).toBe('STRONG')
    expect(screen.getByText('Between outputs.')).toBeInTheDocument()
    expect(screen.getByText('Done').tagName).toBe('STRONG')
    await user.click(screen.getAllByRole('button', { name: 'Copy JSON' })[0])
    expect(await navigator.clipboard.readText()).toBe(first)
  })

  test.each([
    '```python\n{"rows":[1]}\n```',
    'Inline `{"rows":[1]}` stays code.',
    'A [1](https://example.com) link.',
    'Result:\n{"rows":[{"value":1}],"unfinished":\nDone.',
    'Result:\n{"rows":[{"value":1}],invalid:true}\nDone.',
  ])(
    'keeps non-JSON code and malformed outer documents out of the tree: %s',
    (content) => {
      render(
        <RequestTraceConversation
          legs={[
            leg(null, {
              direction: 'client_response',
              rendered: { content, stream: false },
            }),
          ]}
        />
      )
      expect(
        screen.queryByRole('list', { name: 'JSON tree' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: 'Copy JSON' })
      ).not.toBeInTheDocument()
    }
  )

  test.each([
    '{"rows":[{"translation":"Translated text"}]}',
    '```json\n{"rows":[{"translation":"Translated text"}]}\n```',
    'Recorded output:\n\n```json\n{"rows":[{"translation":"Translated text"}]}\n```\n\nEnd of output.',
    '```json\n{"rows":[]}\n```\n\nBetween outputs.\n\n```json\n{"rows":[1]}\n```',
  ])(
    'JSON reply offers nested disclosure without losing message text: %s',
    async (content) => {
      render(
        <RequestTraceConversation
          legs={[
            leg(
              { choices: [{ message: { content } }] },
              { direction: 'client_response' }
            ),
          ]}
        />
      )

      const message = within(screen.getByRole('article', { name: 'Assistant' }))
      const rows = message.getByRole('button', { name: /rows/ })
      expect(rows).toHaveAttribute('aria-expanded', 'true')
      rows.focus()
      await userEvent.keyboard('{Enter}')
      expect(rows).toHaveAttribute('aria-expanded', 'false')
      await userEvent.keyboard('{Enter}')
      expect(rows).toHaveAttribute('aria-expanded', 'true')
      if (content.startsWith('Recorded')) {
        expect(message.getByText('Recorded output:')).toBeInTheDocument()
        expect(message.getByText('End of output.')).toBeInTheDocument()
      }
      if (content.includes('Between outputs.')) {
        expect(message.getByText('Between outputs.')).toBeInTheDocument()
      }
    }
  )

  test('raw JSON body preserves number spelling when copied', async () => {
    const user = userEvent.setup()
    const body = '{ "id": 9007199254740993, "value": -0, "scale": 1e3 }'
    render(
      <RequestTraceRawLeg
        traceId='trace-json'
        leg={leg(null, { body })}
        view='body'
      />
    )

    await user.click(screen.getByRole('button', { name: 'Copy JSON' }))

    expect(await navigator.clipboard.readText()).toBe(body)
    expect(screen.getByText('9007199254740993')).toBeInTheDocument()
    expect(screen.getByText('-0')).toBeInTheDocument()
    expect(screen.getByText('1e3')).toBeInTheDocument()
  })

  test('rendered responses and captured headers use the same JSON disclosure', () => {
    const { rerender } = render(
      <RequestTraceRenderedLeg
        leg={leg(null, {
          rendered: { content: '{"rows":[1,2]}', stream: false },
        })}
        label='Client response'
        showAttempt={false}
      />
    )
    expect(screen.getByRole('button', { name: /rows/ })).toHaveAttribute(
      'aria-expanded'
    )

    rerender(
      <RequestTraceRawLeg
        traceId='trace-json'
        leg={leg(null, { headers: { Accept: ['application/json'] } })}
        view='headers'
      />
    )
    expect(screen.getByRole('button', { name: /Accept/ })).toHaveAttribute(
      'aria-expanded'
    )
  })

  test('long JSON replies expose their tree immediately instead of hiding the message', () => {
    const content = JSON.stringify({
      rows: [{ text: 'Recorded text '.repeat(800) }],
    })
    render(
      <RequestTraceConversation
        legs={[
          leg(null, {
            direction: 'client_response',
            rendered: { content, stream: false },
          }),
        ]}
      />
    )

    expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Expand' })
    ).not.toBeInTheDocument()
    // The JSON viewer owns scrolling; its message must not add another scrollbar.
    expect(
      screen
        .getByRole('article', { name: 'Assistant' })
        .querySelectorAll('.overflow-auto')
    ).toHaveLength(1)
  })

  test('JSON in recorded reasoning uses the same tree when opened', async () => {
    render(
      <RequestTraceConversation
        legs={[
          leg(null, {
            direction: 'client_response',
            rendered: {
              content: 'Answer',
              reasoning: '{"steps":[{"check":"complete"}]}',
              stream: false,
            },
          }),
        ]}
      />
    )
    expect(
      screen.queryByRole('button', { name: /steps/ })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Reasoning' }))
    expect(screen.getByRole('button', { name: /steps/ })).toHaveAttribute(
      'aria-expanded',
      'true'
    )
  })

  test.each(['json', 'quoted-unlabelled', 'unfenced'])(
    'large %s JSON blocks retain surrounding Markdown and stay expandable',
    async (style) => {
      const code = JSON.stringify({
        rows: [{ text: 'Detailed captured content '.repeat(1_000) }],
      })
      let fence = code
      if (style === 'json') {
        fence = `\`\`\`json\n${code}\n\`\`\``
      } else if (style === 'quoted-unlabelled') {
        fence = `> \`\`\`\n> ${code}\n> \`\`\``
      }
      const content = `## Recorded output\n\n${fence}\n\nEnd of output.`
      render(
        <RequestTraceConversation
          legs={[
            leg(null, {
              direction: 'client_response',
              rendered: { content, stream: false },
            }),
          ]}
        />
      )
      await userEvent.click(screen.getByRole('button', { name: 'Expand' }))

      expect(
        screen.getByRole('heading', { name: 'Recorded output' })
      ).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
      expect(screen.getByText('End of output.')).toBeInTheDocument()
    }
  )

  test('tool arguments and matched JSON output retain independent trees inside one card', async () => {
    const user = userEvent.setup()
    const argumentsCode = '{ "id": 9007199254740993, "options": {"limit": 0} }'
    render(
      <RequestTraceConversation
        legs={[
          leg({
            messages: [
              {
                role: 'assistant',
                tool_calls: [
                  {
                    id: 'call_json',
                    type: 'function',
                    function: { name: 'lookup', arguments: argumentsCode },
                  },
                ],
              },
              {
                role: 'tool',
                tool_call_id: 'call_json',
                content: '{"rows":[{"found":false}]}',
              },
            ],
          }),
        ]}
      />
    )
    await user.click(
      screen.getByRole('button', { name: /Tool call lookup call_json/ })
    )

    expect(screen.getByRole('button', { name: /options/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
    expect(screen.getByText('9007199254740993')).toBeInTheDocument()
    expect(
      screen.queryByRole('article', { name: 'Tool' })
    ).not.toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: 'Copy JSON' })[0])
    expect(await navigator.clipboard.readText()).toBe(argumentsCode)
  })

  test('mixed tool output recognizes fenced and unfenced JSON while copying the complete result', async () => {
    const user = userEvent.setup()
    const content =
      'First result:\n{"rows":[{"found":false}]}\n\nSecond result:\n```json\n{"details":[{"count":0}]}\n```\n\nComplete.'
    render(
      <RequestTraceConversation
        legs={[
          leg({
            messages: [
              {
                role: 'assistant',
                tool_calls: [
                  {
                    id: 'call_mixed',
                    type: 'function',
                    function: { name: 'lookup', arguments: '{}' },
                  },
                ],
              },
              {
                role: 'tool',
                tool_call_id: 'call_mixed',
                content,
              },
            ],
          }),
        ]}
      />
    )
    await user.click(
      screen.getByRole('button', { name: /Tool call lookup call_mixed/ })
    )

    expect(screen.getByRole('button', { name: /rows/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /details/ })).toBeInTheDocument()
    expect(screen.getByText('First result:')).toBeInTheDocument()
    expect(screen.getByText('Second result:')).toBeInTheDocument()
    expect(screen.getByText('Complete.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Copy to clipboard' }))
    expect(await navigator.clipboard.readText()).toBe(content)
  })

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
            rawArguments: '{"command":"git branch --show-current"}',
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
            rawArguments: '{"command":"git status --short"}',
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
    const longResult = `Large tool result\n[stdout] **literal** <tag>\n{"unfinished":\n${'output '.repeat(1_300)}`
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
      screen.getByRole('textbox', { name: 'Tool result' })
    ).toHaveTextContent('[stdout] **literal** <tag>')
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

  test('long messages retain newlines when expanded into a bounded plain-text fallback', async () => {
    const longContent = `Large captured message\nLanguage: en_us\n${'context '.repeat(3_000)}`
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
    const content = message.getByText(longContent, {
      normalizer: (text) => text,
    })
    expect(content).toHaveClass('whitespace-pre-wrap')
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
