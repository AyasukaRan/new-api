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
 * Utility functions for usage logs feature
 */
import {
  getAllLogs,
  getUserLogs,
  getAllMidjourneyLogs,
  getUserMidjourneyLogs,
  getAllTaskLogs,
  getUserTaskLogs,
} from '../api'
import {
  LOG_TYPES,
  DISPLAYABLE_LOG_TYPES,
  LOG_TYPE_ENUM,
  TIMING_LOG_TYPES,
} from '../constants'
import type { UsageLog } from '../data/schema'
import type {
  GetLogsParams,
  GetLogsResponse,
  FetchLogsConfig,
  LiveLogSnapshot,
  LogSource,
  GetMidjourneyLogsParams,
  GetTaskLogsParams,
} from '../types'
import { getLogFilterValues, normalizeLogFilterValue } from './filter'
import { getRelativeLogTimeRange } from './time-range'

export { buildQueryParams } from './query-params'

// ============================================================================
// Type Checkers & Utilities
// ============================================================================

/**
 * Check if log type is displayable (has detailed info)
 */
export function isDisplayableLogType(type: number): boolean {
  return (DISPLAYABLE_LOG_TYPES as readonly number[]).includes(type)
}

/** Check whether a log contains consumption or test cost details. */
export function isConsumeLogType(type: number): boolean {
  return type === LOG_TYPE_ENUM.CONSUME || type === LOG_TYPE_ENUM.TEST_CONSUME
}

/** Check if log type shows timing info. */
export function isTimingLogType(type: number): boolean {
  return (TIMING_LOG_TYPES as readonly number[]).includes(type)
}

/**
 * Get log type configuration by type number
 */
export function getLogTypeConfig(type: number, source: LogSource = 'usage') {
  const displayType =
    source === 'test' && isConsumeLogType(type)
      ? LOG_TYPE_ENUM.TEST_CONSUME
      : type
  return LOG_TYPES.find((t) => t.value === displayType) || LOG_TYPES[0]
}

/**
 * Check if log uses per-call billing
 */
export function isPerCallBilling(modelPrice?: number): boolean {
  return (modelPrice ?? 0) > 0
}

/**
 * Get default time range (today 00:00:00 to now)
 */
export function getDefaultTimeRange(): { start: Date; end: Date } {
  const now = new Date()
  const start = new Date(now)
  start.setHours(0, 0, 0, 0)
  return { start, end: now }
}

/**
 * Convert milliseconds timestamp to seconds for API
 */
function timestampToSeconds(ms: number): number {
  return Math.floor(ms / 1000)
}

/**
 * Build time range parameters with default values
 * Shared logic for all log types
 */
function buildTimeRangeParams(
  searchParams: Record<string, unknown>,
  useMilliseconds: boolean
): { start_timestamp?: number; end_timestamp?: number } {
  const hasTimeParams = searchParams.startTime ?? searchParams.endTime
  const defaultTimeRange = !hasTimeParams ? getDefaultTimeRange() : null

  const convertTimestamp = (timestamp: number) =>
    useMilliseconds ? timestamp : timestampToSeconds(timestamp)

  const getTimestamp = (paramTime?: unknown, defaultTime?: Date) => {
    const time = (paramTime as number) || defaultTime?.getTime()
    return time ? convertTimestamp(time) : undefined
  }

  return {
    start_timestamp: getTimestamp(
      searchParams.startTime,
      defaultTimeRange?.start
    ),
    end_timestamp: getTimestamp(searchParams.endTime, defaultTimeRange?.end),
  }
}

/**
 * Build base parameters with time range (for drawing and task logs)
 * @param useMilliseconds - Whether to use millisecond timestamps (true for drawing logs, false for task logs)
 */
export function buildBaseParams(config: {
  page: number
  pageSize: number
  searchParams: Record<string, unknown>
  useMilliseconds?: boolean
}): {
  p: number
  page_size: number
  channel_id?: string
  start_timestamp?: number
  end_timestamp?: number
} {
  const { page, pageSize, searchParams, useMilliseconds = false } = config

  return {
    p: page,
    page_size: pageSize,
    ...(searchParams.channel
      ? {
          channel_id: String(searchParams.channel),
        }
      : {}),
    ...buildTimeRangeParams(searchParams, useMilliseconds),
  }
}

/**
 * Build API params from search params and column filters (for common logs)
 */
export function buildApiParams(config: {
  source?: LogSource
  page: number
  pageSize: number
  searchParams: Record<string, unknown>
  columnFilters?: Array<{ id: string; value: unknown }>
  isAdmin: boolean
}): GetLogsParams {
  const { page, pageSize, searchParams, columnFilters = [], isAdmin } = config

  // Helper to process type parameter (single value from array)
  const processType = (value: unknown): number | undefined => {
    const parseType = (raw: unknown): number | undefined => {
      const type = Number(raw)
      return Number.isFinite(type) ? type : undefined
    }

    if (Array.isArray(value) && value.length === 1) {
      return parseType(value[0])
    }
    if (typeof value === 'string' && value !== '') {
      return parseType(value)
    }
    return undefined
  }

  // Build base params from search params
  const params: GetLogsParams = {
    ...(isAdmin && config.source === 'test' ? { source: 'test' } : {}),
    ...(config.source !== 'test' ? { category: 'model' } : {}),
    ...(config.source !== 'test' &&
    (searchParams.result === 'success' || searchParams.result === 'failure')
      ? { result: searchParams.result }
      : {}),
    p: page,
    page_size: pageSize,
    ...(config.source === 'test' && searchParams.type
      ? { type: processType(searchParams.type) }
      : {}),
    ...(searchParams.model
      ? { model_name: normalizeLogFilterValue(searchParams.model) }
      : {}),
    ...(searchParams.token
      ? { token_name: normalizeLogFilterValue(searchParams.token) }
      : {}),
    ...(searchParams.group
      ? { group: normalizeLogFilterValue(searchParams.group) }
      : {}),
    ...(config.source !== 'test' && searchParams.reasoningEffort
      ? {
          reasoning_effort: normalizeLogFilterValue(
            searchParams.reasoningEffort
          ),
        }
      : {}),
    ...(config.source !== 'test' && isAdmin && searchParams.clientTool
      ? { client_tool: normalizeLogFilterValue(searchParams.clientTool) }
      : {}),
    ...(config.source !== 'test' && searchParams.responseTransport
      ? {
          response_transport: normalizeLogFilterValue(
            searchParams.responseTransport
          ),
        }
      : {}),
    ...(config.source !== 'test' && isAdmin && searchParams.requestPath
      ? { request_path: normalizeLogFilterValue(searchParams.requestPath) }
      : {}),
    ...(config.source !== 'test' && searchParams.ip
      ? { ip: normalizeLogFilterValue(searchParams.ip) }
      : {}),
    ...(isAdmin && searchParams.channel
      ? {
          channel: Array.isArray(searchParams.channel)
            ? getLogFilterValues(searchParams.channel).map(Number)
            : Number(searchParams.channel),
        }
      : {}),
    ...(isAdmin && searchParams.username
      ? { username: normalizeLogFilterValue(searchParams.username) }
      : {}),
    ...(searchParams.requestId
      ? { request_id: String(searchParams.requestId) }
      : {}),
    ...(searchParams.upstreamRequestId
      ? { upstream_request_id: String(searchParams.upstreamRequestId) }
      : {}),
    ...buildTimeRangeParams(searchParams, false),
  }

  // Override with column filters if present
  if (columnFilters.length > 0) {
    columnFilters.forEach(({ id, value }) => {
      if (value === undefined || value === null || value === '') return

      switch (id) {
        case 'type':
          if (config.source === 'test') params.type = processType(value)
          break
        case 'model_name':
          params.model_name = normalizeLogFilterValue(value)
          break
        case 'token_name':
          params.token_name = normalizeLogFilterValue(value)
          break
        case 'group':
          params.group = normalizeLogFilterValue(value)
          break
        case 'channel':
          if (isAdmin) {
            params.channel = Array.isArray(value)
              ? getLogFilterValues(value).map(Number)
              : Number(value)
          }
          break
        case 'username':
          if (isAdmin) params.username = normalizeLogFilterValue(value)
          break
      }
    })
  }

  return params
}

// ============================================================================
// Data Fetching
// ============================================================================

/**
 * Fetch logs based on category type
 */
export async function fetchLogsByCategory(
  config: FetchLogsConfig
): Promise<
  GetLogsResponse & { live?: LiveLogSnapshot; historyFilters?: GetLogsParams }
> {
  const { logCategory, isAdmin, page, pageSize, searchParams } = config

  if (logCategory === 'common') {
    const params = buildApiParams(config)
    // Personal and ClickHouse log IDs are display-only. Ask for stable row
    // keys for paging even when the chosen date range has no live subscription.
    if (config.source !== 'test') params.live = 1
    if (config.livePreset && page === 1) {
      const { start } = getRelativeLogTimeRange(config.livePreset)
      params.start_timestamp = Math.floor(start.getTime() / 1000)
      // Include rows committed up to the cursor even if the client clock lags.
      delete params.end_timestamp
    } else if (params.live && page > 1 && !params.end_timestamp) {
      // Preserve the offset of a manually selected page in an open-ended range.
      params.end_timestamp = Math.floor(Date.now() / 1000)
    }
    const result = await (isAdmin ? getAllLogs : getUserLogs)(params)
    const historyFilters = params.live ? { ...params } : undefined
    if (!config.livePreset || !result.data?.live_cursor) {
      return { ...result, historyFilters }
    }
    delete params.live
    delete params.end_timestamp
    return {
      ...result,
      historyFilters,
      live: {
        cursor: result.data.live_cursor,
        items: result.data.items as UsageLog[],
        total: result.data.total,
        filters: params,
      },
    }
  }

  // For drawing and task logs
  const baseParams = buildBaseParams({
    page,
    pageSize,
    searchParams,
    useMilliseconds: logCategory === 'drawing',
  })

  const paramsWithFilter = {
    ...baseParams,
    ...(logCategory === 'drawing'
      ? { mj_id: searchParams.filter as string | undefined }
      : {}),
    ...(logCategory === 'task'
      ? { task_id: searchParams.filter as string | undefined }
      : {}),
  }

  if (logCategory === 'drawing') {
    return isAdmin
      ? await getAllMidjourneyLogs(paramsWithFilter as GetMidjourneyLogsParams)
      : await getUserMidjourneyLogs(paramsWithFilter as GetMidjourneyLogsParams)
  }

  // task logs
  return isAdmin
    ? await getAllTaskLogs(paramsWithFilter as GetTaskLogsParams)
    : await getUserTaskLogs(paramsWithFilter as GetTaskLogsParams)
}
