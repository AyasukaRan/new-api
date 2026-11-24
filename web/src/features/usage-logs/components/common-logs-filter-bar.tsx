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
import { useQueryClient, useIsFetching } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { Table } from '@tanstack/react-table'
import { Eye, EyeOff, RefreshCw } from 'lucide-react'
import { useState, useCallback, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useMediaQuery } from '@/hooks'

import { LOG_TYPE_ALL_VALUE } from '../constants'
import { useLiveLogRange } from '../hooks/use-live-log-range'
import {
  buildSearchParams,
  getLogTypeValue,
  isValidLogChannelFilter,
  type LogColumnFilterKey,
} from '../lib/filter'
import { getLogTimePreset, getRelativeLogTimeRange } from '../lib/time-range'
import type { CommonLogFilters } from '../types'
import { CommonLogsStats } from './common-logs-stats'
import { CompactDateTimeRangePicker } from './compact-date-time-range-picker'
import { ExportLogsDialog } from './dialogs/export-logs-dialog'
import {
  LogColumnFilterFields,
  LogGroupFilter,
  LogUsernameFilter,
} from './log-column-filter-fields'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from './logs-filter-toolbar'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

type CommonLogDraft = {
  sourceKey: string
  filters: CommonLogFilters
  logType: string
}

function buildSearchSourceKey(values: {
  timeRange?: unknown
  startTime?: unknown
  endTime?: unknown
  channel?: unknown
  model?: unknown
  token?: unknown
  group?: unknown
  username?: unknown
  reasoningEffort?: unknown
  clientTool?: unknown
  responseTransport?: unknown
  requestPath?: unknown
  ip?: unknown
  requestId?: unknown
  upstreamRequestId?: unknown
  type?: unknown
}) {
  return [
    values.timeRange,
    values.timeRange ? undefined : values.startTime,
    values.timeRange ? undefined : values.endTime,
    values.channel,
    values.model,
    values.token,
    values.group,
    values.username,
    values.reasoningEffort,
    values.clientTool,
    values.responseTransport,
    values.requestPath,
    values.ip,
    values.requestId,
    values.upstreamRequestId,
    Array.isArray(values.type) ? values.type.join(',') : values.type,
  ]
    .map((value) => String(value ?? ''))
    .join('\u001f')
}

interface CommonLogsFilterBarProps<TData> {
  columnFiltersInHeader?: boolean
  onLiveTotal?: (total: number) => void
  table: Table<TData>
}

export function CommonLogsFilterBar<TData>(
  props: CommonLogsFilterBarProps<TData>
) {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const searchParams = route.useSearch()
  const livePreset = getLogTimePreset(searchParams)
  const autoRefresh = searchParams.autoRefresh !== false
  const { isAdminView: isAdmin, logSource } = useLogsViewScope()
  const isTest = logSource === 'test'
  const section = isTest ? 'test' : 'common'
  useLiveLogRange(livePreset, isTest && autoRefresh)
  const streamPreset =
    !isTest && autoRefresh && (searchParams.page ?? 1) === 1
      ? livePreset
      : undefined
  const [clock, setClock] = useState(Date.now)
  useEffect(() => {
    if (!streamPreset) return
    const tick = () => {
      if (!document.hidden) setClock(Date.now())
    }
    tick()
    const timer = window.setInterval(tick, 30_000)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [streamPreset])
  const displayedRange = useMemo(
    () =>
      streamPreset && getRelativeLogTimeRange(streamPreset, new Date(clock)),
    [clock, streamPreset]
  )
  const refreshLabel = isTest ? t('Auto refresh (30s)') : t('Auto refresh')
  const { sensitiveVisible, setSensitiveVisible } = useUsageLogsContext()
  const fetchingLogs = useIsFetching({ queryKey: ['logs'] })
  const searchState = useMemo<CommonLogDraft>(() => {
    const { start, end } = getRelativeLogTimeRange(livePreset ?? 'today')
    const sourceValues = {
      timeRange: livePreset,
      startTime: searchParams.startTime,
      endTime: searchParams.endTime,
      channel: searchParams.channel,
      model: searchParams.model,
      token: searchParams.token,
      group: searchParams.group,
      username: searchParams.username,
      reasoningEffort: searchParams.reasoningEffort,
      clientTool: searchParams.clientTool,
      responseTransport: searchParams.responseTransport,
      requestPath: searchParams.requestPath,
      ip: searchParams.ip,
      requestId: searchParams.requestId,
      upstreamRequestId: searchParams.upstreamRequestId,
      type: searchParams.type,
    }
    const filters: CommonLogFilters = {
      timeRange: livePreset,
      startTime: searchParams.startTime
        ? new Date(searchParams.startTime)
        : start,
      endTime: searchParams.endTime ? new Date(searchParams.endTime) : end,
      channel: searchParams.channel || undefined,
      model: searchParams.model || undefined,
      token: searchParams.token || undefined,
      group: searchParams.group || undefined,
      username: searchParams.username || undefined,
      reasoningEffort: searchParams.reasoningEffort || undefined,
      clientTool: searchParams.clientTool || undefined,
      responseTransport: searchParams.responseTransport || undefined,
      requestPath: searchParams.requestPath || undefined,
      ip: searchParams.ip || undefined,
      requestId: searchParams.requestId || undefined,
      upstreamRequestId: searchParams.upstreamRequestId || undefined,
    }
    return {
      sourceKey: buildSearchSourceKey(sourceValues),
      filters,
      logType: getLogTypeValue(searchParams.type),
    }
  }, [
    livePreset,
    searchParams.startTime,
    searchParams.endTime,
    searchParams.channel,
    searchParams.model,
    searchParams.token,
    searchParams.group,
    searchParams.username,
    searchParams.reasoningEffort,
    searchParams.clientTool,
    searchParams.responseTransport,
    searchParams.requestPath,
    searchParams.ip,
    searchParams.requestId,
    searchParams.upstreamRequestId,
    searchParams.type,
  ])
  const [draft, setDraft] = useState<CommonLogDraft>(() => searchState)
  const activeDraft =
    draft.sourceKey === searchState.sourceKey ? draft : searchState
  const filters = useMemo(
    () =>
      activeDraft.filters.timeRange
        ? {
            ...activeDraft.filters,
            startTime: searchState.filters.startTime,
            endTime: searchState.filters.endTime,
          }
        : activeDraft.filters,
    [
      activeDraft.filters,
      searchState.filters.startTime,
      searchState.filters.endTime,
    ]
  )
  const logType = activeDraft.logType

  const handleChange = useCallback(
    (field: keyof CommonLogFilters, value: Date | string | undefined) => {
      setDraft((current) => {
        const base =
          current.sourceKey === searchState.sourceKey ? current : searchState
        return {
          sourceKey: searchState.sourceKey,
          filters: { ...base.filters, [field]: value },
          logType: base.logType,
        }
      })
    },
    [searchState]
  )

  const handleApply = useCallback(
    (nextFilters: CommonLogFilters = filters) => {
      if (isAdmin && !isValidLogChannelFilter(nextFilters.channel)) return
      let resolvedFilters = nextFilters
      if (nextFilters.timeRange) {
        const range = getRelativeLogTimeRange(nextFilters.timeRange)
        resolvedFilters = {
          ...nextFilters,
          startTime: range.start,
          endTime: range.end,
        }
      }
      const filterParams = buildSearchParams(resolvedFilters, 'common')
      navigate({
        to: '/usage-logs/$section',
        params: { section },
        search: {
          ...filterParams,
          pageSize: searchParams.pageSize,
          autoRefresh: nextFilters.timeRange
            ? searchParams.autoRefresh
            : undefined,
          type: isTest ? undefined : [logType],
          page: 1,
        },
      })
      queryClient.invalidateQueries({ queryKey: ['logs'] })
      queryClient.invalidateQueries({ queryKey: ['usage-logs-stats'] })
    },
    [
      filters,
      isAdmin,
      isTest,
      logType,
      navigate,
      queryClient,
      section,
      searchParams.pageSize,
      searchParams.autoRefresh,
    ]
  )

  const handleReset = useCallback(() => {
    const { start, end } = getRelativeLogTimeRange('today')
    const resetFilters: CommonLogFilters = {
      timeRange: 'today',
      startTime: start,
      endTime: end,
    }
    const resetSearch = {
      timeRange: 'today' as const,
      type: isTest ? undefined : [LOG_TYPE_ALL_VALUE],
      startTime: start.getTime(),
      endTime: end.getTime(),
    }
    setDraft({
      sourceKey: buildSearchSourceKey(resetSearch),
      filters: resetFilters,
      logType: LOG_TYPE_ALL_VALUE,
    })

    navigate({
      to: '/usage-logs/$section',
      params: { section },
      search: {
        page: 1,
        ...resetSearch,
      },
    })
    queryClient.invalidateQueries({ queryKey: ['logs'] })
    queryClient.invalidateQueries({ queryKey: ['usage-logs-stats'] })
  }, [isTest, navigate, queryClient, section])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') handleApply()
    },
    [handleApply]
  )

  const inToolbar = (id: string) =>
    !props.columnFiltersInHeader || !props.table.getColumn(id)?.getIsVisible()
  const accountInToolbar = inToolbar('token_name')
  const channelInToolbar = isAdmin && inToolbar('channel')
  const reasoningInToolbar = !isTest && inToolbar('reasoning_effort')
  const sourceInToolbar = !isTest && inToolbar('request_source')
  const modelInToolbar = inToolbar('model_name')
  const typeInToolbar = !isTest && inToolbar('created_at')
  const transportInToolbar = !isTest && inToolbar('is_stream')
  const hasTypeFilter = logType !== LOG_TYPE_ALL_VALUE
  const expandedFilterCount = [
    accountInToolbar && filters.token,
    isAdmin && accountInToolbar && filters.username,
    channelInToolbar && filters.channel,
    reasoningInToolbar && filters.reasoningEffort,
    isAdmin && sourceInToolbar && filters.clientTool,
    sourceInToolbar && filters.ip,
    transportInToolbar && filters.responseTransport,
    transportInToolbar && isAdmin && filters.requestPath,
    filters.requestId,
    isAdmin && filters.upstreamRequestId,
  ].filter(Boolean).length
  const activeFilterCount = [
    filters.token,
    filters.model,
    filters.group,
    hasTypeFilter,
    isAdmin && filters.username,
    isAdmin && filters.channel,
    !isTest && filters.reasoningEffort,
    !isTest && isAdmin && filters.clientTool,
    !isTest && filters.ip,
    !isTest && filters.responseTransport,
    !isTest && isAdmin && filters.requestPath,
    filters.requestId,
    isAdmin && filters.upstreamRequestId,
  ].filter(Boolean).length
  const sensitiveInputClass = sensitiveVisible
    ? undefined
    : '[-webkit-text-security:disc]'
  const statsBar = (
    <CommonLogsStats
      livePreset={streamPreset}
      onLiveTotal={props.onLiveTotal}
    />
  )
  const sensitiveToggle = (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            onClick={() => setSensitiveVisible(!sensitiveVisible)}
            aria-label={sensitiveVisible ? t('Hide') : t('Show')}
            className='text-muted-foreground hover:text-foreground size-7 max-sm:size-11'
          />
        }
      >
        {sensitiveVisible ? <Eye /> : <EyeOff />}
      </TooltipTrigger>
      <TooltipContent>
        {sensitiveVisible ? t('Hide') : t('Show')}
      </TooltipContent>
    </Tooltip>
  )

  const dateRangeFilter = (
    <LogsFilterField wide className='flex items-center gap-1.5'>
      <CompactDateTimeRangePicker
        start={
          (filters.timeRange && displayedRange?.start) || filters.startTime
        }
        end={(filters.timeRange && displayedRange?.end) || filters.endTime}
        preset={filters.timeRange}
        className='min-w-0 flex-1'
        onPresetSelect={(timeRange) => handleApply({ ...filters, timeRange })}
        onChange={({ start, end }) => {
          const nextFilters = {
            ...filters,
            timeRange: undefined,
            startTime: start,
            endTime: end,
          }
          setDraft({
            sourceKey: searchState.sourceKey,
            filters: nextFilters,
            logType,
          })
          if (isMobile) {
            handleApply(nextFilters)
          }
        }}
      />
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              type='button'
              size='icon'
              variant={livePreset && autoRefresh ? 'secondary' : 'ghost'}
              className='size-8 shrink-0'
              disabled={!livePreset}
              aria-label={refreshLabel}
              aria-pressed={Boolean(livePreset && autoRefresh)}
              onClick={() =>
                void navigate({
                  to: '/usage-logs/$section',
                  params: { section },
                  search: (previous) => {
                    if (isTest || !livePreset || (previous.page ?? 1) > 1) {
                      return { ...previous, autoRefresh: !autoRefresh }
                    }
                    const { start, end } = getRelativeLogTimeRange(livePreset)
                    return {
                      ...previous,
                      timeRange: livePreset,
                      startTime: start.getTime(),
                      endTime: end.getTime(),
                      autoRefresh: !autoRefresh,
                    }
                  },
                  replace: true,
                  resetScroll: false,
                })
              }
            />
          }
        >
          <RefreshCw className='size-3.5' />
        </TooltipTrigger>
        <TooltipContent>
          {(searchParams.page ?? 1) > 1
            ? t('Auto refresh pauses after the first page')
            : refreshLabel}
        </TooltipContent>
      </Tooltip>
    </LogsFilterField>
  )
  const inlineSuggestions = isMobile && !isTest
  const columnFieldProps = {
    inlineSuggestions,
    isAdmin,
    values: { ...filters, type: logType },
    onChange: (field: LogColumnFilterKey, value: string) => {
      if (field !== 'type') {
        handleChange(field, value)
        return
      }
      setDraft((current) => {
        const base =
          current.sourceKey === searchState.sourceKey ? current : searchState
        return { ...base, logType: value }
      })
    },
    onKeyDown: handleKeyDown,
    maskClass: sensitiveInputClass,
  }
  const modelFilter = (
    <LogColumnFilterFields column='model' {...columnFieldProps} />
  )
  const groupFilter = (
    <LogGroupFilter
      inlineSuggestions={inlineSuggestions}
      isAdmin={isAdmin}
      value={filters.group}
      maskClass={sensitiveInputClass}
      onValueChange={(value) => handleChange('group', value)}
      onKeyDown={handleKeyDown}
    />
  )
  const typeFilter = (
    <LogColumnFilterFields column='time' {...columnFieldProps} />
  )
  const advancedFilters = (
    <>
      {accountInToolbar && (
        <LogsFilterField>
          <LogsFilterInput
            placeholder={t('Token Name')}
            aria-label={t('Token Name')}
            className={sensitiveInputClass}
            value={filters.token || ''}
            onChange={(e) => handleChange('token', e.target.value)}
            onKeyDown={handleKeyDown}
          />
        </LogsFilterField>
      )}
      {accountInToolbar && isAdmin && (
        <LogUsernameFilter
          inlineSuggestions={inlineSuggestions}
          value={filters.username}
          maskClass={sensitiveInputClass}
          onValueChange={(value) => handleChange('username', value)}
          onKeyDown={handleKeyDown}
        />
      )}
      {channelInToolbar && (
        <LogColumnFilterFields column='channel' {...columnFieldProps} />
      )}
      {reasoningInToolbar && (
        <LogColumnFilterFields column='reasoning' {...columnFieldProps} />
      )}
      {transportInToolbar && (
        <LogColumnFilterFields column='transport' {...columnFieldProps} />
      )}
      {sourceInToolbar && (
        <LogColumnFilterFields column='source' {...columnFieldProps} />
      )}
      <LogsFilterField>
        <LogsFilterInput
          placeholder={t('Request ID')}
          value={filters.requestId || ''}
          onChange={(e) => handleChange('requestId', e.target.value)}
          onKeyDown={handleKeyDown}
        />
      </LogsFilterField>
      {isAdmin && (
        <LogsFilterField>
          <LogsFilterInput
            placeholder={t('Upstream Request ID')}
            value={filters.upstreamRequestId || ''}
            onChange={(e) => handleChange('upstreamRequestId', e.target.value)}
            onKeyDown={handleKeyDown}
          />
        </LogsFilterField>
      )}
    </>
  )

  return (
    <LogsFilterToolbar
      table={props.table}
      compactMobile
      commonUsage={!isTest}
      stats={statsBar}
      actionStart={sensitiveToggle}
      actionEnd={<ExportLogsDialog livePreset={streamPreset} />}
      primaryFilters={
        <>
          {dateRangeFilter}
          {modelInToolbar && modelFilter}
          {accountInToolbar && groupFilter}
          {typeInToolbar && typeFilter}
        </>
      }
      advancedFilters={advancedFilters}
      mobilePinnedFilters={dateRangeFilter}
      mobileFilters={
        <>
          {modelFilter}
          {groupFilter}
          {!isTest && typeFilter}
          {advancedFilters}
        </>
      }
      mobileFilterCount={activeFilterCount}
      hasAdvancedActiveFilters={expandedFilterCount > 0}
      advancedFilterCount={expandedFilterCount}
      hasActiveFilters={activeFilterCount > 0}
      onSearch={() => handleApply()}
      searchLoading={fetchingLogs > 0}
      searchDisabled={isAdmin && !isValidLogChannelFilter(filters.channel)}
      onReset={handleReset}
    />
  )
}
