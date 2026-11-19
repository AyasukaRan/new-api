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
import {
  pairConversationTools,
  type TraceConversationMessage,
  type TraceConversationSource,
  type TraceToolContentPart,
} from '../lib/request-trace-conversation'
import type { TurnCapture } from './api'

// Normalized messages come from the same trace parser for every wire format.
// A replay can omit private reasoning while echoing the same assistant reply.
function historyKey(
  message: TraceConversationMessage,
  images: Map<string, number>
) {
  // Compare full image strings exactly, but serialize only a small local ID
  // when matching long, replayed prompts and screenshots in tool results.
  const contentKey = (part: TraceToolContentPart) => {
    if (part.type !== 'image' || !part.src) return part
    let id = images.get(part.src)
    if (id === undefined) {
      id = images.size
      images.set(part.src, id)
    }
    return ['image', id]
  }
  const visible = message.parts.filter((p) => p.type !== 'reasoning')
  return JSON.stringify([
    message.role,
    (visible.length ? visible : message.parts).map((part) => {
      if (part.type === 'tool-call' || part.type === 'tool-result') {
        return [
          part.type,
          part.id,
          part.name,
          part.contentParts ? part.contentParts.map(contentKey) : part.value,
          part.isError,
        ]
      }
      return part.type === 'image' ? contentKey(part) : part
    }),
  ])
}

// Find a contiguous suffix/prefix in linear time, including sliding-window histories.
function overlap(previous: string[], next: string[]) {
  if (!next.length) return 0
  const lengths = new Uint32Array(next.length)
  let matched = 0
  for (let i = 1; i < next.length; i++) {
    while (matched && next[i] !== next[matched]) matched = lengths[matched - 1]
    if (next[i] === next[matched]) matched++
    lengths[i] = matched
  }
  matched = 0
  for (
    let i = Math.max(0, previous.length - next.length);
    i < previous.length;
    i++
  ) {
    while (matched && previous[i] !== next[matched]) {
      matched = lengths[matched - 1]
    }
    if (previous[i] === next[matched]) matched++
  }
  return matched
}

/** Subtract only replayed history from the immediately preceding turn on this
 * thread. Keep the original capture immutable for neighboring timeline nodes. */
export function recordedTurnSources(
  current: TurnCapture,
  previous?: TurnCapture
): TraceConversationSource[] {
  if (!previous) return pairConversationTools(current.sources)
  const images = new Map<string, number>()
  const keyFor = (message: TraceConversationMessage) =>
    historyKey(message, images)
  const prior = previous.sources.flatMap((source) => source.messages)
  const history = prior
    .filter((m) => m.role !== 'system' && m.role !== 'developer')
    .map(keyFor)
  const instructions = new Set(
    prior
      .filter((m) => m.role === 'system' || m.role === 'developer')
      .map(keyFor)
  )
  const sources = current.sources.map((source) => {
    if (!source.leg.direction.endsWith('_request')) return source
    let start = 0
    const front: TraceConversationMessage[] = []
    while (start < source.messages.length) {
      const message = source.messages[start]
      if (message.role !== 'system' && message.role !== 'developer') break
      if (!instructions.has(keyFor(message))) front.push(message)
      start++
    }
    const messages = source.messages.slice(start)
    let shared = 0
    // An identical user prompt can be a genuinely new turn.
    if (messages.some((m) => m.role === 'assistant' || m.role === 'tool')) {
      const keys = messages.map(keyFor)
      shared = overlap(history, keys)
      // When an echoed answer has been edited, retain its changed tail.
      if (!shared) {
        while (shared < keys.length && keys[shared] === history[shared]) {
          shared++
        }
      }
    }
    return { ...source, messages: [...front, ...messages.slice(shared)] }
  })
  // Pair tools only within the visible node, so a result that refers to a
  // call in a collapsed older turn stays readable here.
  return pairConversationTools(sources)
}
