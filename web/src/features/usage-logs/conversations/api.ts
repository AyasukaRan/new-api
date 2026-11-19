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
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import { getRequestTrace } from '../api'
import {
  conversationSource,
  selectConversationLegs,
  type TraceConversationSource,
} from '../lib/request-trace-conversation'
import { readTracePayload } from '../lib/request-trace-stream'

export interface ConversationRecord {
  conversation_id: string
  parent_id: string
  agent_name: string
  title: string
  model_name: string
  user_id: number
  client_tool?: string
  started_at: number
  updated_at: number
  turn_count: number
}

export interface ConversationFilters {
  user_id?: number
  model_name?: string
  client_tool?: string
  client_tool_unknown?: boolean
  min_turns?: number
  max_turns?: number
  hide_greetings?: boolean
}

export interface ConversationSort {
  sort_by: 'updated_at' | 'started_at' | 'turn_count'
  sort_order: 'asc' | 'desc'
}

export interface RecordedTurn {
  turn_id?: string
  parent_turn_id?: string
  conversation_id: string
  parent_id: string
  agent_name: string
  started_at_ms: number
  trace_id: string
  request_id: string
  model_name: string
  created_at: number
}

interface CursorPage<T> {
  items: T[]
  next_cursor: string
}

async function getPage<T>(
  path: string,
  params: Record<string, number | string | boolean | undefined>,
  signal: AbortSignal
) {
  const { data } = await api.get<{ success: boolean; data?: CursorPage<T> }>(
    path,
    { params, signal, disableDuplicate: true }
  )
  if (!data.success || !Array.isArray(data.data?.items)) {
    throw createServerError(data)
  }
  return data.data
}

export const getConversationRecords = (
  cursor: string,
  signal: AbortSignal,
  filters: ConversationFilters,
  sort: ConversationSort
) =>
  getPage<ConversationRecord>(
    '/api/log/conversations',
    { cursor, limit: 30, ...filters, ...sort },
    signal
  )

export async function getConversationClients(signal: AbortSignal) {
  const { data } = await api.get<{ success: boolean; data?: string[] }>(
    '/api/log/conversations/clients',
    { signal, disableDuplicate: true }
  )
  if (!data.success || !Array.isArray(data.data)) throw createServerError(data)
  return data.data
}

export const getRecordedTurns = (
  conversationId: string,
  cursor: string,
  signal: AbortSignal
) =>
  getPage<RecordedTurn>(
    '/api/log/conversations/turns',
    {
      conversation_id: conversationId,
      cursor,
      limit: 30,
    },
    signal
  )

export interface TurnCapture {
  traceId: string
  sources: TraceConversationSource[]
}

export async function getTurnCapture(
  traceId: string,
  signal: AbortSignal,
  onPartial?: (capture: TurnCapture) => void
): Promise<TurnCapture> {
  const legs = selectConversationLegs(await getRequestTrace(traceId, signal))
  const partial = new Map<number, TraceConversationSource>()
  const sources = await Promise.all(
    legs.map(async (leg) => {
      if (!leg.has_body) return conversationSource(leg)
      const output = leg.direction.endsWith('_response')
      const progress = (value: { source?: TraceConversationSource }) => {
        if (
          !onPartial ||
          !value.source ||
          partial.get(leg.seq) === value.source ||
          signal.aborted
        ) {
          return
        }
        partial.set(leg.seq, value.source)
        onPartial({
          traceId,
          sources: legs.flatMap((item) => partial.get(item.seq) ?? []),
        })
      }
      let payload = await readTracePayload(
        traceId,
        leg,
        output ? 'rendered' : 'conversation',
        signal,
        progress
      )
      if (output && !payload.leg.rendered) {
        payload = await readTracePayload(
          traceId,
          leg,
          'conversation',
          signal,
          progress
        )
      }
      return payload.source ?? conversationSource(payload.leg)
    })
  )
  return { traceId, sources }
}
