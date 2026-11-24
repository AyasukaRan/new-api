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
/**
 * Utility functions for usage logs filters
 */
import {
  LOG_CATEGORY_LABELS,
  LOG_TYPE_ALL_VALUE,
  LOG_TYPE_FILTERS,
} from '../constants'
import type {
  LogCategory,
  LogFilters,
  CommonLogFilters,
  DrawingLogFilters,
  TaskLogFilters,
} from '../types'

export function getLogTypeValue(value: unknown): string {
  return Array.isArray(value) &&
    value.length === 1 &&
    typeof value[0] === 'string' &&
    LOG_TYPE_FILTERS.some((type) => type.value === value[0])
    ? value[0]
    : LOG_TYPE_ALL_VALUE
}

// ============================================================================
// Filter Building Functions
// ============================================================================

/**
 * Build search params from filters based on log category
 */
export function buildSearchParams(
  filters: LogFilters,
  logCategory: LogCategory
): Record<string, unknown> {
  const baseParams: Record<string, unknown> = {
    ...(filters.startTime && { startTime: filters.startTime.getTime() }),
    ...(filters.endTime && { endTime: filters.endTime.getTime() }),
    ...(filters.channel && { channel: filters.channel }),
  }

  switch (logCategory) {
    case 'common': {
      const commonFilters = filters as CommonLogFilters
      return {
        ...baseParams,
        ...(commonFilters.timeRange && { timeRange: commonFilters.timeRange }),
        ...(commonFilters.model && { model: commonFilters.model }),
        ...(commonFilters.token && { token: commonFilters.token }),
        ...(commonFilters.group && { group: commonFilters.group }),
        ...(commonFilters.username && { username: commonFilters.username }),
        ...(commonFilters.reasoningEffort && {
          reasoningEffort: commonFilters.reasoningEffort,
        }),
        ...(commonFilters.clientTool && {
          clientTool: commonFilters.clientTool,
        }),
        ...(commonFilters.responseTransport && {
          responseTransport: commonFilters.responseTransport,
        }),
        ...(commonFilters.requestPath && {
          requestPath: commonFilters.requestPath,
        }),
        ...(commonFilters.ip && { ip: commonFilters.ip }),
        ...(commonFilters.requestId && { requestId: commonFilters.requestId }),
        ...(commonFilters.upstreamRequestId && {
          upstreamRequestId: commonFilters.upstreamRequestId,
        }),
      }
    }
    case 'drawing': {
      const drawingFilters = filters as DrawingLogFilters
      return {
        ...baseParams,
        ...(drawingFilters.mjId && { filter: drawingFilters.mjId }),
      }
    }
    case 'task': {
      const taskFilters = filters as TaskLogFilters
      return {
        ...baseParams,
        ...(taskFilters.taskId && { filter: taskFilters.taskId }),
      }
    }
    default:
      return baseParams
  }
}

/**
 * Get log category display name
 */
export function getLogCategoryLabel(category: LogCategory): string {
  return LOG_CATEGORY_LABELS[category]
}

const columnKeys = {
  time: ['type'],
  transport: ['responseTransport', 'requestPath'],
  channel: ['channel'],
  identity: ['username', 'token', 'group'],
  model: ['model'],
  reasoning: ['reasoningEffort'],
  source: ['clientTool', 'ip'],
} as const
export type LogFilterColumn = keyof typeof columnKeys
export type LogColumnFilterKey = (typeof columnKeys)[LogFilterColumn][number]
export type LogColumnFilterValues = Partial<Record<LogColumnFilterKey, string>>

export function isValidLogChannelFilter(value?: string) {
  const text = value?.trim() ?? ''
  return (
    !text ||
    (/^\d+$/.test(text) &&
      Number(text) > 0 &&
      Number.isSafeInteger(Number(text)))
  )
}

export function getLogColumnFilterKeys(
  column: LogFilterColumn,
  isAdmin: boolean
) {
  return columnKeys[column].filter(
    (key) =>
      isAdmin ||
      !['channel', 'username', 'clientTool', 'requestPath'].includes(key)
  )
}
