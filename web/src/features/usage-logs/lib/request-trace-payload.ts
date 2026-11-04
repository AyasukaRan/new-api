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

import type { RequestTraceLeg, RequestTraceRendered } from '../types'
import {
  conversationSource,
  type TraceConversationSource,
} from './request-trace-conversation'

export type TracePayloadView = 'conversation' | 'rendered' | 'body'
export interface TracePayload {
  leg: RequestTraceLeg
  source?: TraceConversationSource
  download?: Blob
}
export type TraceWorkerRequest =
  | { type: 'start'; leg: RequestTraceLeg; view: TracePayloadView }
  | { type: 'chunk'; chunk: Uint8Array }
  | { type: 'end' }
export type TraceWorkerReply =
  | { type: 'source'; source: TraceConversationSource }
  | { type: 'messages'; messages: TraceConversationSource['messages'] }
  | { type: 'done'; payload: TracePayload }
  | { type: 'error' }

/** Runs in the worker: no full-payload JSON parsing on the browser UI thread. */
export function parseTracePayload(
  leg: RequestTraceLeg,
  view: TracePayloadView,
  body: string
): TracePayload {
  if (view === 'body') {
    // Large raw payloads remain downloadable in full without building a giant JSON tree.
    const large = body.length > 200_000
    return {
      leg: { ...leg, body: large ? body.slice(0, 200_000) : body },
      ...(large
        ? { download: new Blob([body], { type: 'text/plain;charset=utf-8' }) }
        : {}),
    }
  }
  const parsedLeg = { ...leg, body }
  if (view === 'rendered') {
    const envelope = JSON.parse(body) as {
      success?: boolean
      data?: { rendered?: RequestTraceRendered | null }
    }
    if (
      envelope.success !== true ||
      !envelope.data ||
      !('rendered' in envelope.data)
    ) {
      throw new Error('Failed to load the request trace')
    }
    parsedLeg.body = ''
    parsedLeg.rendered = envelope.data.rendered ?? undefined
  }
  const source = conversationSource(parsedLeg)
  source.hasAlternatives ||= parsedLeg.rendered?.has_alternatives === true
  source.rawFallback ||= source.hasAlternatives
  const metadata = { ...parsedLeg, body: '' }
  return { leg: metadata, source: { ...source, leg: metadata } }
}

/** Single-pass scanner for complete members of a top-level conversation array.
 * Carries only unfinished member fragments; strings/escapes can cross any chunk.
 * The final parser still decides complete-document semantics and warnings.
 */
export class TraceHistoryStreamParser {
  private depth = 0
  private quoted = false
  private escaped = false
  private key = ''
  private collectingKey = false
  private expectsKey = false
  private arrayKey: string | undefined
  private active = false
  private completed = false
  private member = false
  private fragments: string[] = []
  constructor(private leg: RequestTraceLeg) {}

  push(text: string): TraceConversationSource['messages'] {
    const messages: TraceConversationSource['messages'] = []
    if (this.completed) return messages
    let start = 0
    for (let index = 0; index < text.length; index++) {
      const char = text[index]
      if (
        this.active &&
        !this.member &&
        this.depth === 2 &&
        !/[\s,\]]/.test(char)
      ) {
        this.member = true
        start = index
      }
      if (this.quoted) {
        if (this.escaped) this.escaped = false
        else if (char === '\\') this.escaped = true
        else if (char === '"') this.quoted = false
        else if (this.collectingKey) this.key += char
        continue
      }
      if (this.active && this.depth === 2 && (char === ',' || char === ']')) {
        if (this.member) {
          this.fragments.push(text.slice(start, index))
          const body = `{"${this.arrayKey}":[${this.fragments.join('')}]}`
          messages.push(
            ...conversationSource({ ...this.leg, body, truncated: false })
              .messages
          )
          this.fragments = []
          this.member = false
        }
        if (char === ']') {
          this.completed = true
          break
        }
        continue
      }
      if (char === '"') {
        this.quoted = true
        this.collectingKey = this.depth === 1 && this.expectsKey
        if (this.collectingKey) this.key = ''
      } else if (char === ':') {
        if (this.depth === 1) this.expectsKey = false
      } else if (char === ',' && this.depth === 1) {
        this.expectsKey = true
      } else if (char === '{') {
        this.depth++
        if (this.depth === 1) this.expectsKey = true
      } else if (char === '[') {
        if (
          this.depth === 1 &&
          ['messages', 'input', 'contents'].includes(this.key)
        ) {
          this.active = true
          this.arrayKey = this.key
        }
        this.depth++
      } else if (char === '}' || char === ']') this.depth--
    }
    if (this.member && !this.completed) this.fragments.push(text.slice(start))
    return messages
  }
}

/** Worker message processor is shared with deterministic stream/cancellation tests. */
export function createTracePayloadProcessor(
  post: (reply: TraceWorkerReply) => void
) {
  let initial: Extract<TraceWorkerRequest, { type: 'start' }> | undefined
  let chunks: string[] = []
  const decoder = new TextDecoder()
  let history: TraceHistoryStreamParser | undefined
  let started = false
  return (message: TraceWorkerRequest) => {
    if (message.type === 'start') {
      initial = message
      if (
        message.view === 'conversation' &&
        message.leg.direction.endsWith('_request')
      ) {
        history = new TraceHistoryStreamParser(message.leg)
      }
    } else if (message.type === 'chunk') {
      const text = decoder.decode(message.chunk, { stream: true })
      chunks.push(text)
      const messages = history?.push(text)
      if (messages?.length && initial) {
        if (!started) {
          post({
            type: 'source',
            source: {
              leg: initial.leg,
              messages: [],
              rawFallback: false,
              referencedHistory: false,
              hasAlternatives: false,
            },
          })
          started = true
        }
        for (let index = 0; index < messages.length; index += 24) {
          post({
            type: 'messages',
            messages: messages.slice(index, index + 24),
          })
        }
      }
    } else if (initial) {
      try {
        chunks.push(decoder.decode())
        const payload = parseTracePayload(
          initial.leg,
          initial.view,
          chunks.join('')
        )
        chunks = []
        // Final validation replaces the streamed prefix atomically; never clear visible history.
        post({ type: 'done', payload })
      } catch {
        post({ type: 'error' })
      }
    }
  }
}
