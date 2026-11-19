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

import { describe, expect, it } from 'vitest'

import { recordedTurnSources } from '../../conversations/transcript'
import type { RequestTraceLeg } from '../../types'
import {
  conversationSource,
  pairConversationTools,
} from '../request-trace-conversation'
import {
  parseTracePayload,
  TraceHistoryStreamParser,
} from '../request-trace-payload'

const png =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jL4cAAAAASUVORK5CYII='
const src = `data:image/png;base64,${png}`
const image = { type: 'image', src }
const capture: RequestTraceLeg = {
  seq: 0,
  attempt: 0,
  direction: 'client_request',
  channel_id: 0,
  format: 'openai',
  method: 'POST',
  url: '',
  status: 0,
  headers: null,
  body: '',
  body_size: 0,
  truncated: false,
  has_object: false,
}

describe('captured conversation images', () => {
  it.each([
    {
      format: 'openai',
      body: {
        messages: [
          {
            role: 'user',
            content: [{ type: 'image_url', image_url: { url: src } }],
          },
        ],
      },
    },
    {
      format: 'openai_responses',
      body: {
        input: [
          { role: 'user', content: [{ type: 'input_image', image_url: src }] },
        ],
      },
    },
    {
      format: 'claude',
      body: {
        messages: [
          {
            role: 'user',
            content: [
              {
                type: 'image',
                source: { type: 'base64', data: png, media_type: 'image/png' },
              },
            ],
          },
        ],
      },
    },
    {
      format: 'gemini',
      body: {
        contents: [
          { parts: [{ inlineData: { data: png, mimeType: 'image/png' } }] },
        ],
      },
    },
    {
      format: 'gemini',
      body: {
        contents: [
          { parts: [{ inline_data: { data: png, mime_type: 'image/png' } }] },
        ],
      },
    },
    { format: 'openai_responses', body: { input: ` \n${src}\n` } },
  ])(
    'reads a complete $format image from retained input',
    ({ format, body }) => {
      const source = conversationSource({
        ...capture,
        format,
        body: JSON.stringify(body),
      })
      expect(source.messages).toEqual([{ role: 'user', parts: [image] }])
    }
  )

  it('reassembles an image split between history chunks and keeps mixed content order', () => {
    const body = JSON.stringify({
      messages: [
        {
          role: 'user',
          content: [
            { type: 'text', text: 'first' },
            { type: 'image_url', image_url: { url: src } },
            { type: 'text', text: 'last' },
          ],
        },
      ],
    })
    const cut = body.indexOf(png) + 7
    const stream = new TraceHistoryStreamParser(capture)
    expect(stream.push(body.slice(0, cut))).toEqual([])
    expect(stream.push(body.slice(cut))).toEqual([
      {
        role: 'user',
        parts: [
          { type: 'text', text: 'first' },
          image,
          { type: 'text', text: 'last' },
        ],
      },
    ])
  })

  it('removes replayed image history but retains a tool result whose screenshot changed', () => {
    const gif =
      'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=='
    const captured = (screenshot: string) => ({
      traceId: 'node',
      sources: [
        conversationSource({
          ...capture,
          format: 'openai_responses',
          body: JSON.stringify({
            input: [
              {
                role: 'user',
                content: [{ type: 'input_image', image_url: src }],
              },
              {
                type: 'function_call_output',
                call_id: 'screen',
                output: [{ type: 'input_image', image_url: screenshot }],
              },
            ],
          }),
        }),
      ],
    })
    const prior = captured(src)
    expect(recordedTurnSources(captured(src), prior)[0].messages).toEqual([])
    const changed = recordedTurnSources(captured(gif), prior)[0].messages
    expect(changed).toHaveLength(1)
    expect(changed[0].parts[0]).toMatchObject({
      type: 'tool-result',
      contentParts: [{ type: 'image', src: gif }],
    })
  })

  it('renders a paired multimodal tool result and preserves MCP content beside its picture', () => {
    const output = JSON.stringify({
      content: [
        { type: 'text', text: 'Screen 1' },
        { type: 'image', mimeType: 'image/png', data: png },
      ],
      isError: false,
    })
    const body = JSON.stringify({
      input: [
        { type: 'function_call', name: 'view_image', call_id: 'call-1' },
        { type: 'function_call_output', call_id: 'call-1', output },
      ],
    })
    const [source] = pairConversationTools([
      conversationSource({ ...capture, format: 'openai_responses', body }),
    ])
    const contentParts = [
      { type: 'text', text: 'Screen 1' },
      image,
      { type: 'raw', value: { isError: false } },
    ]
    expect(source.messages[0].parts[0]).toMatchObject({
      type: 'tool-call',
      linked: true,
      result: { value: null, contentParts },
    })
    expect(source.messages[1].parts[0]).toMatchObject({
      type: 'tool-result',
      linked: true,
      value: null,
      contentParts,
    })
  })

  it('parses generated and captured rendered outputs without losing assistant text', () => {
    const response: RequestTraceLeg = {
      ...capture,
      direction: 'client_response',
      format: 'openai_responses',
    }
    const raw = conversationSource({
      ...response,
      body: JSON.stringify({
        output: [{ type: 'image_generation_call', result: png }],
      }),
    })
    expect(raw.messages).toEqual([{ role: 'assistant', parts: [image] }])
    const body = JSON.stringify({
      success: true,
      data: {
        rendered: {
          stream: true,
          content: 'done',
          images: [{ data: png, media_type: 'image/png' }, { data: '' }],
        },
      },
    })
    const rendered = parseTracePayload(response, 'rendered', body)
    expect(rendered.source?.messages).toEqual([
      {
        role: 'assistant',
        parts: [{ type: 'text', text: 'done' }, image, { type: 'image' }],
      },
    ])
  })

  it('keeps external references and code as data; invalid or unsupported local images lack a preview URL', () => {
    const external = {
      type: 'image_url',
      image_url: { url: 'https://example.test/private.png' },
    }
    const code = `Use this URL: ${src}`
    const body = JSON.stringify({
      messages: [
        {
          role: 'user',
          content: [
            external,
            { type: 'text', text: code },
            ...[
              `data:image/jpeg;base64,${png}`,
              'data:image/svg+xml;base64,PHN2Zz4=',
              'data:image/png;base64,iVBORw0KGgoAAAAN[truncated]',
              `${src}AA`,
            ].map((url) => ({ type: 'input_image', image_url: url })),
          ],
        },
      ],
    })
    expect(conversationSource({ ...capture, body }).messages[0].parts).toEqual([
      { type: 'raw', value: external },
      { type: 'text', text: code },
      { type: 'image' },
      { type: 'image' },
      { type: 'image' },
      { type: 'image' },
    ])
  })
})
