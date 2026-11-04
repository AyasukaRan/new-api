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
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { buildRequestTraceConversation } from '../../lib/request-trace-conversation'
import {
  createTracePayloadProcessor,
  parseTracePayload,
  TraceHistoryStreamParser,
  type TraceWorkerReply,
  type TraceWorkerRequest,
} from '../../lib/request-trace-payload'
import { readTracePayload } from '../../lib/request-trace-stream'
import type { RequestTraceLeg } from '../../types'
import { RequestTraceSection } from '../dialogs/request-trace-section'

const instances: LocalWorker[] = []
class LocalWorker {
  onReply: ((event: MessageEvent<TraceWorkerReply>) => void) | null = null
  terminate = vi.fn()
  receive = createTracePayloadProcessor((data) => {
    if (!this.terminate.mock.calls.length) {
      this.onReply?.({ data } as MessageEvent<TraceWorkerReply>)
    }
  })
  constructor() {
    instances.push(this)
  }
  addEventListener(
    type: string,
    listener: (event: MessageEvent<TraceWorkerReply>) => void
  ) {
    if (type === 'message') this.onReply = listener
  }
  postMessage(message: TraceWorkerRequest) {
    queueMicrotask(() => {
      if (!this.terminate.mock.calls.length) this.receive(message)
    })
  }
}
function leg(overrides: Partial<RequestTraceLeg> = {}): RequestTraceLeg {
  return {
    seq: 0,
    attempt: 0,
    direction: 'client_request',
    channel_id: 0,
    format: 'openai',
    method: 'POST',
    url: '',
    status: 200,
    headers: null,
    body: '',
    has_body: true,
    body_size: 10000,
    truncated: false,
    has_object: false,
    ...overrides,
  }
}
const encoder = new TextEncoder()
function bodyStream(body: string) {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(body))
      controller.close()
    },
  })
}
const clients: QueryClient[] = []
function renderTrace(traceId = 'long-trace') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <RequestTraceSection traceId={traceId} enabled />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  instances.length = 0
  vi.stubGlobal('Worker', LocalWorker)
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(
    function (this: HTMLElement) {
      return this.getAttribute('role') === 'log' ? 600 : 120
    }
  )
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(900)
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
    configurable: true,
    value: vi.fn(),
  })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
})
afterEach(() => {
  cleanup()
  for (const client of clients) client.clear()
  clients.length = 0
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset()
})

describe('streamed request trace loading', () => {
  test('shows completed history and the final reply while the request stream is still open; other legs load only on selection', async () => {
    let input: ReadableStreamDefaultController<Uint8Array> | undefined
    const requests: { seq: number; view?: string }[] = []
    const legs = [
      leg(),
      leg({ seq: 1, direction: 'upstream_request' }),
      leg({ seq: 2, direction: 'client_response' }),
    ]
    vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
      if (url === '/api/log/trace') {
        return { data: { success: true, data: { legs } } }
      }
      const params = config?.params as { seq: number; view?: string }
      requests.push(params)
      if (params.seq === 0) {
        return {
          data: new ReadableStream<Uint8Array>({
            start(controller) {
              input = controller
            },
          }),
        }
      }
      if (params.seq === 1) {
        return {
          data: bodyStream(
            '{"messages":[{"role":"user","content":"Upstream only"}]}'
          ),
        }
      }
      return {
        data: bodyStream(
          JSON.stringify({
            success: true,
            data: {
              rendered: { content: 'Reply arrives first', stream: true },
            },
          })
        ),
      }
    })
    renderTrace()
    await userEvent.click(
      screen.getByRole('button', { name: 'View request trace' })
    )
    expect(await screen.findByText('Reply arrives first')).toBeInTheDocument()
    await waitFor(() => expect(input).toBeDefined())
    await act(async () => {
      input?.enqueue(
        encoder.encode(
          '{"messages":[{"role":"user","content":"First complete history message"},'
        )
      )
    })
    expect(
      await screen.findByText('First complete history message')
    ).toBeInTheDocument()
    expect(
      screen.getByRole('status', { name: 'Input history' })
    ).toBeInTheDocument()
    expect(requests.map((request) => request.seq).sort()).toEqual([0, 2])
    await act(async () => {
      input?.enqueue(
        encoder.encode(
          '{"role":"assistant","content":"Last history message"}]}'
        )
      )
      input?.close()
    })
    expect(await screen.findByText('Last history message')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.queryByRole('status', { name: 'Input history' })
      ).not.toBeInTheDocument()
    )
    await userEvent.click(screen.getByRole('tab', { name: 'Exchange details' }))
    await userEvent.click(
      screen.getByRole('tab', { name: 'new-api → Upstream · Attempt 1' })
    )
    await waitFor(() =>
      expect(requests.some((request) => request.seq === 1)).toBe(true)
    )
  })

  test('closing cancels the pending reader and worker; reopening starts fresh without old history', async () => {
    const cancel = vi.fn()
    let count = 0
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/log/trace') {
        return { data: { success: true, data: { legs: [leg()] } } }
      }
      count++
      if (count > 1) {
        return {
          data: bodyStream(
            '{"messages":[{"role":"user","content":"Fresh reopened history"}]}'
          ),
        }
      }
      return {
        data: new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(
              encoder.encode(
                '{"messages":[{"role":"user","content":"Old pending history"},'
              )
            )
          },
          cancel,
        }),
      }
    })
    renderTrace()
    await userEvent.click(
      screen.getByRole('button', { name: 'View request trace' })
    )
    expect(await screen.findByText('Old pending history')).toBeInTheDocument()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(cancel).toHaveBeenCalledOnce())
    expect(instances[0].terminate).toHaveBeenCalled()
    await userEvent.click(
      screen.getByRole('button', { name: 'View request trace' })
    )
    expect(
      await screen.findByText('Fresh reopened history')
    ).toBeInTheDocument()
    expect(screen.queryByText('Old pending history')).not.toBeInTheDocument()
  })

  test('a missing rendered response falls back to that leg body without fetching unrelated exchanges', async () => {
    const views: (string | undefined)[] = []
    vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
      if (url === '/api/log/trace') {
        return {
          data: {
            success: true,
            data: { legs: [leg({ direction: 'client_response' })] },
          },
        }
      }
      const view = (config?.params as { view?: string } | undefined)?.view
      views.push(view)
      return {
        data: bodyStream(
          view
            ? '{"success":true,"data":{"rendered":null}}'
            : '{"choices":[{"message":{"role":"assistant","content":"Recovered raw reply"}}]}'
        ),
      }
    })
    renderTrace()
    await userEvent.click(
      screen.getByRole('button', { name: 'View request trace' })
    )
    expect(await screen.findByText('Recovered raw reply')).toBeInTheDocument()
    expect(views).toEqual(['rendered', undefined])
  })

  test('a corrupted body response shows retry and never treats it as an empty conversation', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/log/trace') {
        return { data: { success: true, data: { legs: [leg()] } } }
      }
      throw new Error('Storage integrity check failed')
    })
    renderTrace()
    await userEvent.click(
      screen.getByRole('button', { name: 'View request trace' })
    )
    expect(
      await screen.findByRole('button', { name: 'Retry' })
    ).toBeInTheDocument()
    expect(
      screen.queryByText('No conversation was captured.')
    ).not.toBeInTheDocument()
  })
})

describe('frame-batched stream publication', () => {
  test('a rapid burst publishes one complete prefix before EOF and final validation retains every message', async () => {
    const frames = new Map<number, FrameRequestCallback>()
    let frameId = 0
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      frames.set(++frameId, callback)
      return frameId
    })
    vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
    let controller: ReadableStreamDefaultController<Uint8Array> | undefined
    vi.spyOn(api, 'get').mockResolvedValue({
      data: new ReadableStream<Uint8Array>({
        start(stream) {
          controller = stream
        },
      }),
    })
    const progress = vi.fn()
    const result = readTracePayload(
      'burst',
      leg(),
      'conversation',
      new AbortController().signal,
      progress
    )
    const messages = Array.from({ length: 240 }, (_, index) => ({
      role: 'user',
      content: `Burst message ${index}`,
    }))
    controller?.enqueue(
      encoder.encode(
        `{"messages":[${messages.map((message) => JSON.stringify(message)).join(',')},`
      )
    )
    await waitFor(() => expect(frames.size).toBe(1))
    expect(progress).not.toHaveBeenCalled()
    for (const [id, callback] of frames) {
      frames.delete(id)
      callback(performance.now())
    }
    expect(progress).toHaveBeenCalledTimes(1)
    expect(progress.mock.calls[0][0].source.messages).toHaveLength(240)
    controller?.enqueue(
      encoder.encode('{"role":"assistant","content":"Final history member"}]}')
    )
    controller?.close()
    const complete = await result
    expect(complete.source?.messages).toHaveLength(241)
    expect(complete.source?.messages.at(-1)?.parts).toEqual([
      { type: 'text', text: 'Final history member' },
    ])
    expect(frames.size).toBe(0)
    expect(progress).toHaveBeenCalledTimes(1)
  })

  test.each(['abort', 'read-error'])(
    '%s discards an unpublished frame and prevents stale progress after cleanup',
    async (reason) => {
      const frames = new Map<number, FrameRequestCallback>()
      vi.stubGlobal(
        'requestAnimationFrame',
        (callback: FrameRequestCallback) => {
          frames.set(1, callback)
          return 1
        }
      )
      vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
      let controller: ReadableStreamDefaultController<Uint8Array> | undefined
      const cancel = vi.fn()
      vi.spyOn(api, 'get').mockResolvedValue({
        data: new ReadableStream<Uint8Array>({
          start(stream) {
            controller = stream
          },
          cancel,
        }),
      })
      const abort = new AbortController()
      const progress = vi.fn()
      const result = readTracePayload(
        'cancel-burst',
        leg(),
        'conversation',
        abort.signal,
        progress
      )
      const failed = expect(result).rejects.toThrow()
      controller?.enqueue(
        encoder.encode(
          '{"messages":[{"role":"user","content":"Never publish this stale message"},'
        )
      )
      await waitFor(() => expect(frames.size).toBe(1))
      const stale = frames.get(1)
      if (reason === 'abort') abort.abort()
      else controller?.error(new Error('Broken connection'))
      await failed
      stale?.(performance.now())
      expect(progress).not.toHaveBeenCalled()
      expect(frames.size).toBe(0)
      expect(instances[0].terminate).toHaveBeenCalled()
      if (reason === 'abort') expect(cancel).toHaveBeenCalledOnce()
    }
  )
})

describe('worker conversation normalization', () => {
  test.each(['messages', 'input', 'contents'])(
    'incremental %s preserves nested strings and chunk-boundary escapes',
    (field) => {
      const message =
        field === 'contents'
          ? {
              role: 'user',
              parts: [{ text: '中文 "quoted" ] { \\ newline\n' }],
            }
          : { role: 'user', content: '中文 "quoted" ] { \\ newline\n' }
      const body = JSON.stringify({
        metadata: { messages: ['ignore nested arrays'] },
        [field]: [message, message],
      })
      const metadata = leg({
        format: (
          {
            contents: 'gemini',
            input: 'openai_responses',
            messages: 'openai',
          } as Record<string, string>
        )[field],
      })
      const parser = new TraceHistoryStreamParser(metadata)
      const incremental = [...body].flatMap((character) =>
        parser.push(character)
      )
      expect(incremental).toEqual(
        parseTracePayload(metadata, 'conversation', body).source?.messages
      )
      expect(incremental).toHaveLength(2)
    }
  )

  test('a long multi-tool history has the same pairing and raw arguments as the original parser', () => {
    const messages = Array.from({ length: 72 }, (_, index) => [
      { role: 'user', content: `Question ${index}` },
      {
        role: 'assistant',
        tool_calls: [
          {
            id: `call-${index}`,
            type: 'function',
            function: {
              name: 'lookup',
              arguments: JSON.stringify({ query: `value-${index}` }),
            },
          },
        ],
      },
      {
        role: 'tool',
        tool_call_id: `call-${index}`,
        content: JSON.stringify({ result: index }),
      },
      { role: 'assistant', content: `Answer ${index}` },
    ]).flat()
    const body = JSON.stringify({ messages })
    const metadata = leg({ body })
    const source = parseTracePayload(metadata, 'conversation', body).source
    if (!source) throw new Error('Expected parsed source')
    expect(source.messages).toHaveLength(288)
    expect(
      buildRequestTraceConversation([metadata])[0].messages[1].parts[0]
    ).toMatchObject({
      type: 'tool-call',
      linked: true,
      result: { value: '{"result":0}' },
    })
    const streamed = new TraceHistoryStreamParser(metadata)
    const halfway = Math.floor(body.length / 2)
    const first = streamed.push(body.slice(0, halfway))
    expect(first.length).toBeGreaterThan(100)
    expect([...first, ...streamed.push(body.slice(halfway))]).toEqual(
      source.messages
    )
  })

  test('legacy truncated requests keep complete leading messages and the incomplete warning', () => {
    const body =
      '{"messages":[{"role":"user","content":"Kept message"},{"role":"assistant","content":"lost' +
      '\n\n... [12345 bytes elided] ...\n\n' +
      ' tail"}]}'
    const payload = parseTracePayload(
      leg({ truncated: true }),
      'conversation',
      body
    )
    expect(payload.source?.messages).toEqual([
      { role: 'user', parts: [{ type: 'text', text: 'Kept message' }] },
    ])
    expect(payload.source?.rawFallback).toBe(true)
    expect(payload.source?.leg.truncated).toBe(true)
  })

  test('UTF-8 split across bytes loads progressively and final validation does not clear visible history', () => {
    const visibleCounts: number[] = []
    let messages: unknown[] = []
    const receive = createTracePayloadProcessor((reply) => {
      if (reply.type === 'source') messages = reply.source.messages
      if (reply.type === 'messages') messages = [...messages, ...reply.messages]
      if (reply.type === 'done') {
        messages = reply.payload.source?.messages ?? messages
      }
      visibleCounts.push(messages.length)
    })
    receive({ type: 'start', leg: leg(), view: 'conversation' })
    const body = JSON.stringify({
      messages: [
        { role: 'user', content: '中文🙂' },
        { role: 'assistant', content: 'Done' },
      ],
    })
    for (const byte of encoder.encode(body)) {
      receive({ type: 'chunk', chunk: Uint8Array.of(byte) })
    }
    expect(messages).toHaveLength(2)
    receive({ type: 'end' })
    expect(messages).toEqual(
      parseTracePayload(leg(), 'conversation', body).source?.messages
    )
    expect(
      visibleCounts.every(
        (count, index) => index === 0 || count >= visibleCounts[index - 1]
      )
    ).toBe(true)
  })

  test('rendered multiple alternatives stay marked and large raw bodies remain downloadable without a JSON tree', () => {
    const rendered = parseTracePayload(
      leg({ direction: 'client_response' }),
      'rendered',
      JSON.stringify({
        success: true,
        data: {
          rendered: {
            content: 'First alternative',
            stream: false,
            has_alternatives: true,
          },
        },
      })
    )
    expect(rendered.source?.hasAlternatives).toBe(true)
    const body = JSON.stringify({ content: 'x'.repeat(250_000) })
    const raw = parseTracePayload(leg(), 'body', body)
    expect(raw.leg.body.length).toBeLessThan(body.length)
    expect(raw.download?.size).toBe(new TextEncoder().encode(body).length)
  })
})
