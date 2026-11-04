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

import { getRequestTraceBodyStream } from '../api'
import type { RequestTraceLeg } from '../types'
import type { TraceConversationSource } from './request-trace-conversation'
import type {
  TracePayload,
  TracePayloadView,
  TraceWorkerReply,
  TraceWorkerRequest,
} from './request-trace-payload'

export interface TracePayloadProgress {
  bytes: number
  source?: TraceConversationSource
}

/** One reader and worker per active payload; query cancellation owns both. */
export async function readTracePayload(
  traceId: string,
  leg: RequestTraceLeg,
  view: TracePayloadView,
  signal: AbortSignal,
  progress: (value: TracePayloadProgress) => void
): Promise<TracePayload> {
  const stream = await getRequestTraceBodyStream(
    traceId,
    leg.seq,
    view === 'rendered' ? 'rendered' : 'body',
    signal
  )
  if (signal.aborted) {
    await stream.cancel().catch(() => undefined)
    signal.throwIfAborted()
  }
  const reader = stream.getReader()
  let worker: Worker
  try {
    worker = new Worker(
      new URL('./request-trace-payload.worker.ts', import.meta.url),
      { type: 'module' }
    )
  } catch (error) {
    await reader.cancel().catch(() => undefined)
    reader.releaseLock()
    throw error
  }
  let source: TraceConversationSource | undefined
  let bytes = 0
  let pendingMessages: TraceConversationSource['messages'][] = []
  let frame: number | undefined
  let finished = false
  const cancelProgress = () => {
    finished = true
    if (frame !== undefined) cancelAnimationFrame(frame)
    frame = undefined
    pendingMessages = []
  }
  const scheduleProgress = () => {
    if (finished || frame !== undefined) return
    frame = requestAnimationFrame(() => {
      frame = undefined
      if (finished || signal.aborted) return
      if (source && pendingMessages.length > 0) {
        const messages = [...source.messages]
        for (const batch of pendingMessages) messages.push(...batch)
        source = { ...source, messages }
        pendingMessages = []
      }
      progress({ bytes, source })
    })
  }
  let rejectResult: (error: unknown) => void = () => undefined
  const result = new Promise<TracePayload>((resolve, reject) => {
    rejectResult = reject
    worker.addEventListener(
      'message',
      (event: MessageEvent<TraceWorkerReply>) => {
        if (finished || signal.aborted) return
        const message = event.data
        if (message.type === 'error') {
          cancelProgress()
          void reader.cancel().catch(() => undefined)
          reject(new Error('Failed to load the request trace'))
        } else if (message.type === 'source') source = message.source
        else if (message.type === 'messages' && source) {
          pendingMessages.push(message.messages)
          scheduleProgress()
        } else if (message.type === 'done') {
          cancelProgress()
          resolve({
            ...message.payload,
            source: message.payload.source ?? source,
          })
        }
      }
    )
    worker.addEventListener('error', () => {
      cancelProgress()
      void reader.cancel().catch(() => undefined)
      reject(new Error('Failed to load the request trace'))
    })
  })
  // Attach immediately: abort can reject while reader.read() is still pending.
  void result.catch(() => undefined)
  const abort = () => {
    cancelProgress()
    worker.terminate()
    void reader.cancel().catch(() => undefined)
    rejectResult(signal.reason ?? new DOMException('Aborted', 'AbortError'))
  }
  signal.addEventListener('abort', abort, { once: true })
  try {
    signal.throwIfAborted()
    worker.postMessage({
      type: 'start',
      leg,
      view,
    } satisfies TraceWorkerRequest)
    while (true) {
      const chunk = await reader.read()
      signal.throwIfAborted()
      if (chunk.done) break
      bytes += chunk.value.byteLength
      scheduleProgress()
      worker.postMessage(
        { type: 'chunk', chunk: chunk.value } satisfies TraceWorkerRequest,
        [chunk.value.buffer]
      )
    }
    worker.postMessage({ type: 'end' } satisfies TraceWorkerRequest)
    return await result
  } finally {
    cancelProgress()
    signal.removeEventListener('abort', abort)
    worker.terminate()
    await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}
