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
import type { RequestTraceLeg } from '../types'

export type TraceConversationPart =
  | { type: 'text'; text: string }
  | { type: 'reasoning'; text: string }
  | { type: 'raw'; value: unknown }
  | {
      type: 'tool-call' | 'tool-result'
      id?: string
      name?: string
      value: unknown
      linked?: boolean
      isError?: boolean
      result?: { value: unknown; isError?: boolean; name?: string }
    }

export interface TraceConversationMessage {
  role: 'system' | 'developer' | 'user' | 'assistant' | 'tool' | 'unknown'
  parts: TraceConversationPart[]
}

export interface TraceConversationSource {
  leg: RequestTraceLeg
  messages: TraceConversationMessage[]
  rawFallback: boolean
  referencedHistory: boolean
  hasAlternatives: boolean
}

function record(value: unknown): Record<string, unknown> | undefined {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return undefined
  }
  return value as Record<string, unknown>
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

/** Alternatives can arrive together or as separate indexed stream events. */
function hasResponseAlternatives(value: unknown): boolean {
  const documents = Array.isArray(value) ? value : [value]
  return documents.some((document) => {
    const body = record(document)
    const candidates = body?.choices ?? body?.candidates
    if (!Array.isArray(candidates)) return false
    return (
      candidates.length > 1 ||
      candidates.some((candidate) => {
        const index = record(candidate)?.index
        return typeof index === 'number' && index > 0
      })
    )
  })
}

/** Arguments may be an object or an unfinished JSON string in a saved stream. */
function toolValue(value: unknown): unknown {
  if (typeof value !== 'string') return value ?? null
  try {
    return JSON.parse(value)
  } catch {
    return value
  }
}

/** Keep unknown and multimodal blocks inspectable instead of silently dropping them. */
function conversationParts(content: unknown): TraceConversationPart[] {
  if (typeof content === 'string') return [{ type: 'text', text: content }]
  if (content === null || content === undefined) return []
  if (!Array.isArray(content)) return [{ type: 'raw', value: content }]

  return content.map((value): TraceConversationPart => {
    if (typeof value === 'string') return { type: 'text', text: value }
    const part = record(value)
    if (!part) return { type: 'raw', value }

    if (
      typeof part.text === 'string' &&
      (part.type === undefined ||
        [
          'text',
          'input_text',
          'output_text',
          'summary_text',
          'reasoning_text',
        ].includes(String(part.type)))
    ) {
      return {
        type:
          part.thought === true || part.type === 'reasoning_text'
            ? 'reasoning'
            : 'text',
        text: part.text,
      }
    }
    if (part.type === 'thinking' && typeof part.thinking === 'string') {
      return { type: 'reasoning', text: part.thinking }
    }
    if (part.type === 'refusal' && typeof part.refusal === 'string') {
      return { type: 'text', text: part.refusal }
    }

    const call = record(part.functionCall)
    if (call) {
      return {
        type: 'tool-call',
        id: optionalString(call.id),
        name: optionalString(call.name),
        value: call.args ?? null,
      }
    }
    const result = record(part.functionResponse)
    if (result) {
      return {
        type: 'tool-result',
        id: optionalString(result.id),
        name: optionalString(result.name),
        value: result.response ?? null,
      }
    }
    if (
      ['tool_use', 'function_call', 'custom_tool_call'].includes(
        String(part.type)
      )
    ) {
      return {
        type: 'tool-call',
        id: optionalString(part.call_id) ?? optionalString(part.id),
        name: optionalString(part.name),
        value: toolValue(part.arguments ?? part.input),
      }
    }
    if (
      [
        'tool_result',
        'function_call_output',
        'custom_tool_call_output',
      ].includes(String(part.type))
    ) {
      return {
        type: 'tool-result',
        id: optionalString(part.call_id) ?? optionalString(part.tool_use_id),
        value: part.output ?? part.content ?? null,
        isError: part.is_error === true,
      }
    }
    return { type: 'raw', value }
  })
}

/** Splitting Claude/Gemini result blocks keeps tool results out of user bubbles. */
function conversationMessages(items: unknown[]): TraceConversationMessage[] {
  const messages: TraceConversationMessage[] = []
  for (const value of items) {
    const item = record(value)
    if (!item) {
      messages.push({ role: 'unknown', parts: [{ type: 'raw', value }] })
      continue
    }

    let role: TraceConversationMessage['role'] = 'unknown'
    if (item.role === 'model') role = 'assistant'
    if (item.role === 'function') role = 'tool'
    if (
      ['system', 'developer', 'user', 'assistant', 'tool'].includes(
        String(item.role)
      )
    ) {
      role = item.role as TraceConversationMessage['role']
    }
    let parts: TraceConversationPart[]
    if (item.type === 'reasoning') {
      role = 'assistant'
      const content = conversationParts(item.content)
      parts = (
        content.length > 0 ? content : conversationParts(item.summary)
      ).map((part) =>
        part.type === 'text' ? { type: 'reasoning', text: part.text } : part
      )
      if (parts.length === 0) parts = [{ type: 'raw', value }]
    } else if (
      [
        'function_call',
        'custom_tool_call',
        'function_call_output',
        'custom_tool_call_output',
      ].includes(String(item.type))
    ) {
      role = String(item.type).endsWith('_output') ? 'tool' : 'assistant'
      parts = conversationParts([item])
    } else if (role === 'unknown') {
      parts = [{ type: 'raw', value }]
    } else if (role === 'tool') {
      parts = [
        {
          type: 'tool-result',
          id: optionalString(item.tool_call_id),
          name: optionalString(item.name),
          value: item.content ?? null,
        },
      ]
    } else {
      parts = conversationParts(item.content ?? item.parts)
      const reasoning = item.reasoning_content ?? item.reasoning
      if (typeof reasoning === 'string' && reasoning) {
        parts.unshift({ type: 'reasoning', text: reasoning })
      }
      if (Array.isArray(item.tool_calls)) {
        for (const value of item.tool_calls) {
          const call = record(value)
          const fn = record(call?.function)
          if (!fn) {
            parts.push({ type: 'raw', value })
            continue
          }
          parts.push({
            type: 'tool-call',
            id: optionalString(call?.id),
            name: optionalString(fn.name),
            value: toolValue(fn.arguments),
          })
        }
      }
      const legacyCall = record(item.function_call)
      if (legacyCall) {
        parts.push({
          type: 'tool-call',
          name: optionalString(legacyCall.name),
          value: toolValue(legacyCall.arguments),
        })
      }
    }

    if (parts.length === 0) parts = [{ type: 'raw', value }]
    let current: TraceConversationMessage | undefined
    for (const part of parts) {
      const partRole = part.type === 'tool-result' ? 'tool' : role
      if (!current || current.role !== partRole) {
        current = { role: partRole, parts: [] }
        messages.push(current)
      }
      current.parts.push(part)
    }
  }
  return messages
}

interface CapturedJsonValue {
  start: number
  end: number
  value: unknown
}

/** Find one complete JSON value, without completing strings or nested objects. */
function capturedJsonValue(
  text: string,
  position: number,
  reverse = false
): CapturedJsonValue | undefined {
  const step = reverse ? -1 : 1
  let cursor = position
  while (/\s/.test(text[cursor] ?? '') && cursor >= 0 && cursor < text.length) {
    cursor += step
  }
  const boundary = cursor
  const first = text[cursor]
  if (first === undefined) return undefined
  const stack: string[] = []
  let inString = false
  let complete = false
  const structured =
    first === '"' ||
    (reverse ? first === '}' || first === ']' : first === '{' || first === '[')

  for (; cursor >= 0 && cursor < text.length; cursor += step) {
    const character = text[cursor]
    if (!structured) {
      if (/[\s,:{}[\]]/.test(character)) break
      continue
    }
    if (character === '"') {
      let slash = cursor - 1
      while (slash >= 0 && text[slash] === '\\') slash--
      if ((cursor - slash - 1) % 2 !== 0) continue
      inString = !inString
      if (!inString && stack.length === 0) {
        cursor += step
        complete = true
        break
      }
      continue
    }
    if (inString) continue
    const opens = reverse ? '}]' : '{['
    const closes = reverse ? '{[' : '}]'
    const opening = opens.indexOf(character)
    if (opening !== -1) {
      stack.push(closes[opening])
      continue
    }
    if (closes.includes(character)) {
      if (stack.pop() !== character) return undefined
      if (stack.length === 0) {
        cursor += step
        complete = true
        break
      }
    }
  }
  if (structured && !complete) return undefined
  // A number/literal ending at the cut could be only part of its original token.
  if (!structured && (cursor < 0 || cursor >= text.length)) return undefined
  const start = reverse ? cursor + 1 : boundary
  const end = reverse ? boundary + 1 : cursor
  if (end <= start) return undefined
  try {
    return { start, end, value: JSON.parse(text.slice(start, end)) }
  } catch {
    return undefined
  }
}

/**
 * Only recover root fields and complete items in an anchored history array.
 * A suffix that lost its field name could belong to metadata or tool schemas;
 * looking for message-shaped objects there would invent conversation history.
 */
function partialRequestBody(
  leg: RequestTraceLeg
): Record<string, unknown> | undefined {
  const markers = [
    ...leg.body.matchAll(/\n\n\.\.\. \[[1-9]\d* bytes elided\] \.\.\.\n\n/g),
  ]
  if (markers.length !== 1) return undefined
  const marker = markers[0]
  const head = leg.body.slice(0, marker.index).trimStart()
  const tail = leg.body.slice(marker.index + marker[0].length).trimEnd()
  if (!head.startsWith('{') || !tail.endsWith('}')) return undefined
  const historyArrays: Record<string, string> = {
    openai: 'messages',
    claude: 'messages',
    openai_responses: 'input',
    openai_responses_compaction: 'input',
    gemini: 'contents',
  }
  const arrayKey = historyArrays[leg.format]
  if (!arrayKey) return undefined
  const body: Record<string, unknown> = Object.create(null)
  let cursor = 1

  while (cursor < head.length) {
    const key = capturedJsonValue(head, cursor)
    if (!key || typeof key.value !== 'string') break
    cursor = key.end
    while (/\s/.test(head[cursor] ?? '') && cursor < head.length) cursor++
    if (head[cursor++] !== ':') break
    while (/\s/.test(head[cursor] ?? '') && cursor < head.length) cursor++
    const value = capturedJsonValue(head, cursor)
    if (!value) {
      if (key.value === arrayKey && head[cursor] === '[') {
        const items: unknown[] = []
        cursor++
        while (cursor < head.length) {
          const item = capturedJsonValue(head, cursor)
          if (!item) break
          cursor = item.end
          while (/\s/.test(head[cursor] ?? '') && cursor < head.length) cursor++
          if (
            cursor < head.length &&
            head[cursor] !== ',' &&
            head[cursor] !== ']'
          ) {
            break
          }
          items.push(item.value)
          if (head[cursor++] !== ',') break
        }
        body[arrayKey] = items
      }
      break
    }
    cursor = value.end
    while (/\s/.test(head[cursor] ?? '') && cursor < head.length) cursor++
    if (cursor < head.length && head[cursor] !== ',' && head[cursor] !== '}') {
      break
    }
    body[key.value] = value.value
    if (head[cursor++] !== ',') break
  }

  // Parse backwards from the retained root closing brace. A value is usable
  // only when its entire root field name and colon also survived the cut.
  cursor = tail.length - 2
  const seen = new Set<string>()
  while (cursor >= 0) {
    const value = capturedJsonValue(tail, cursor, true)
    if (!value) break
    cursor = value.start - 1
    while (/\s/.test(tail[cursor] ?? '') && cursor >= 0) cursor--
    if (tail[cursor--] !== ':') break
    const key = capturedJsonValue(tail, cursor, true)
    if (!key || typeof key.value !== 'string') break
    cursor = key.start - 1
    while (/\s/.test(tail[cursor] ?? '') && cursor >= 0) cursor--
    if (cursor >= 0 && tail[cursor] !== ',' && tail[cursor] !== '{') break
    if (!seen.has(key.value)) body[key.value] = value.value
    seen.add(key.value)
    if (tail[cursor--] !== ',') break
  }
  return body
}

function conversationSource(leg: RequestTraceLeg): TraceConversationSource {
  const source: TraceConversationSource = {
    leg,
    messages: [],
    rawFallback: false,
    referencedHistory: false,
    hasAlternatives: false,
  }
  const isInput = leg.direction.endsWith('_request')
  let body: Record<string, unknown> | undefined
  try {
    const parsed: unknown = JSON.parse(leg.body)
    body = record(parsed)
    source.hasAlternatives = !isInput && hasResponseAlternatives(parsed)
  } catch {
    if (isInput && leg.truncated) body = partialRequestBody(leg)
    // Captured SSE and partial replies are rendered by the backend below.
  }

  if (!isInput && !body && !source.hasAlternatives) {
    // Only detect alternative indexes; the backend remains the stream renderer.
    for (const line of leg.body.split('\n')) {
      const trimmed = line.trim()
      if (!trimmed.startsWith('data:')) continue
      try {
        if (!hasResponseAlternatives(JSON.parse(trimmed.slice(5)))) continue
        source.hasAlternatives = true
        break
      } catch {
        // [DONE], truncated frames and other non-JSON data are not candidates.
      }
    }
  }

  let items: unknown[] = []
  if (body && isInput) {
    switch (leg.format) {
      case 'openai':
      case 'claude':
        if (body.system !== undefined) {
          items.push({ role: 'system', content: body.system })
        }
        if (Array.isArray(body.messages)) items.push(...body.messages)
        break
      case 'openai_responses':
      case 'openai_responses_compaction':
        if (body.instructions !== undefined) {
          items.push({ role: 'system', content: body.instructions })
        }
        if (typeof body.input === 'string') {
          items.push({ role: 'user', content: body.input })
        }
        if (Array.isArray(body.input)) items.push(...body.input)
        source.referencedHistory = Boolean(
          body.previous_response_id || body.conversation
        )
        break
      case 'gemini': {
        const system = record(body.system_instruction ?? body.systemInstruction)
        if (system) items.push({ ...system, role: 'system' })
        if (Array.isArray(body.contents)) {
          for (const value of body.contents) {
            const content = record(value)
            // Gemini treats an omitted role as user content.
            items.push(
              content && content.role === undefined
                ? { ...content, role: 'user' }
                : value
            )
          }
        }
        source.referencedHistory = Boolean(
          body.cachedContent || body.cached_content
        )
        break
      }
    }
  } else if (body && !isInput) {
    switch (leg.format) {
      case 'openai':
        if (Array.isArray(body.choices)) {
          const primary =
            body.choices.find((value) => record(value)?.index === 0) ??
            body.choices[0]
          const message = record(record(primary)?.message)
          if (message) items.push({ ...message, role: 'assistant' })
        }
        break
      case 'claude':
        if (Array.isArray(body.content)) {
          items.push({ ...body, role: 'assistant' })
        }
        break
      case 'openai_responses':
      case 'openai_responses_compaction':
        if (Array.isArray(body.output)) items = body.output
        break
      case 'gemini':
        if (Array.isArray(body.candidates)) {
          const primary =
            body.candidates.find((value) => record(value)?.index === 0) ??
            body.candidates[0]
          const content = record(record(primary)?.content)
          if (content) items.push({ ...content, role: 'assistant' })
        }
        break
    }
  }
  source.messages = conversationMessages(items)

  if (!isInput && source.messages.length === 0 && leg.rendered) {
    const rendered = leg.rendered
    const parts: TraceConversationPart[] = []
    if (rendered.reasoning) {
      parts.push({ type: 'reasoning', text: rendered.reasoning })
    }
    if (rendered.content) parts.push({ type: 'text', text: rendered.content })
    for (const call of rendered.tool_calls ?? []) {
      parts.push({
        type: 'tool-call',
        id: call.id,
        name: call.name,
        value: toolValue(call.arguments),
      })
    }
    if (parts.length > 0) source.messages.push({ role: 'assistant', parts })
  }
  source.rawFallback =
    leg.truncated || source.hasAlternatives || source.messages.length === 0
  return source
}

/** One captured request's history plus its final reply; retries are not turns. */
export function buildRequestTraceConversation(
  legs: RequestTraceLeg[]
): TraceConversationSource[] {
  const sorted = [...legs].sort((a, b) => a.seq - b.seq)
  const clientInput = sorted.find((leg) => leg.direction === 'client_request')
  const reversed = [...sorted].reverse()
  const clientOutput = reversed.find(
    (leg) => leg.direction === 'client_response'
  )
  const finalAttempt = reversed.find((leg) =>
    leg.direction.startsWith('upstream_')
  )?.attempt
  const upstreamOutput = reversed.find(
    (leg) =>
      leg.direction === 'upstream_response' && leg.attempt === finalAttempt
  )
  const upstreamInput = reversed.find(
    (leg) =>
      leg.direction === 'upstream_request' && leg.attempt === finalAttempt
  )
  const input = clientInput ?? upstreamInput
  const output = clientOutput ?? upstreamOutput
  const sources = [input, output]
    .filter((leg): leg is RequestTraceLeg => Boolean(leg))
    .map(conversationSource)

  type ToolPart = Extract<
    TraceConversationPart,
    { type: 'tool-call' | 'tool-result' }
  >
  const calls = new Map<string, ToolPart[]>()
  const matchedResults = new Map<ToolPart, ToolPart>()
  const ambiguousCalls = new Set<ToolPart>()
  const inferredResultNames = new Set<ToolPart>()
  for (const source of sources) {
    for (const message of source.messages) {
      for (const part of message.parts) {
        if (part.type !== 'tool-call' && part.type !== 'tool-result') continue
        let identity: string
        if (part.id) identity = `id:${part.id}`
        else if (part.name) identity = `name:${part.name}`
        else continue
        const candidates = calls.get(identity) ?? []
        if (part.type === 'tool-call') {
          candidates.push(part)
          calls.set(identity, candidates)
          continue
        }
        const matching = candidates.filter(
          (call) => !call.linked && !ambiguousCalls.has(call)
        )
        // Repeated pending ids or names are ambiguous; don't invent a pairing.
        if (matching.length !== 1) {
          const previousCall = candidates.at(-1)
          const previousResult =
            previousCall && matchedResults.get(previousCall)
          if (matching.length === 0 && previousCall && previousResult) {
            // Two results for one call cannot be treated as one proven pair.
            // Preserve both results independently rather than hiding either.
            delete previousCall.linked
            delete previousCall.result
            delete previousResult.linked
            if (inferredResultNames.delete(previousResult)) {
              delete previousResult.name
            }
            matchedResults.delete(previousCall)
            ambiguousCalls.add(previousCall)
          }
          continue
        }
        const call = matching[0]
        if (part.name && call.name && part.name !== call.name) {
          ambiguousCalls.add(call)
          continue
        }
        call.linked = true
        call.result = {
          value: part.value,
          ...(part.isError ? { isError: true } : {}),
          ...(part.name ? { name: part.name } : {}),
        }
        part.linked = true
        if (!part.name && call.name) inferredResultNames.add(part)
        part.name ??= call.name
        matchedResults.set(call, part)
      }
    }
    // Missing history can contain intervening calls with reused ids. Do not
    // associate a later source's result with a call across a capture gap.
    if (source.leg.truncated) {
      calls.clear()
      matchedResults.clear()
      ambiguousCalls.clear()
      inferredResultNames.clear()
    }
  }
  return sources
}
