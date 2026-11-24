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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import {
  useCallback,
  useEffectEvent,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePage,
  DataTableRow,
  useDataTable,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  getAdminPlans,
  getSelfSubscriptionFull,
} from '@/features/subscriptions/api'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState, type NavigateFn } from '@/hooks/use-table-url-state'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  DEFAULT_LOGS_DATA,
  LOG_TYPE_ALL_VALUE,
  LOG_TYPE_ENUM,
} from '../constants'
import type { UsageLog } from '../data/schema'
import { useLiveUsageLogs } from '../hooks/use-live-usage-logs'
import { useUsageLogHistory } from '../hooks/use-usage-log-history'
import { shouldShowBillingSource } from '../lib/billing-source'
import { useColumnsByCategory } from '../lib/columns'
import { getLogMetadata } from '../lib/format'
import { getLogTimePreset, getRelativeLogTimeRange } from '../lib/time-range'
import { fetchLogsByCategory } from '../lib/utils'
import type { LogCategory, UsageLogDetailSelection } from '../types'
import { CommonLogsFilterBar } from './common-logs-filter-bar'
import { DetailsDialog } from './dialogs/details-dialog'
import { TaskLogsFilterBar } from './task-logs-filter-bar'
import { UsageLogsMobileList } from './usage-logs-mobile-card'
import { useLogsViewScope, type LogsViewAccess } from './usage-logs-provider'

import styles from './common-logs-table.module.css'

const route = getRouteApi('/_authenticated/usage-logs/$section')

const commonColumnVisibility = { request_path: true }
const scrollViewport = '[data-slot="data-table-scroll"]'

function hasOpenLogDialog() {
  return !!document.querySelector(
    '[role="dialog"]:not([hidden]),[role="alertdialog"]:not([hidden])'
  )
}

function atScrollBottom(viewport: HTMLElement) {
  return (
    viewport.clientHeight > 0 &&
    viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 1
  )
}

const logTypeRowTint: Record<number, string> = {
  [LOG_TYPE_ENUM.ERROR]: 'bg-rose-50/40 dark:bg-rose-950/20',
  [LOG_TYPE_ENUM.REFUND]: 'bg-blue-50/30 dark:bg-blue-950/15',
}

// Warning tint for logs where a quota conversion saturated (admin-only marker).
// Takes precedence over the per-type tint since it flags a billing anomaly.
const quotaSaturationRowTint = 'bg-amber-50/60 dark:bg-amber-950/25'

function getColumnVisibilityStorageKey(
  logCategory: LogCategory,
  viewAccess: LogsViewAccess
): string {
  return `usage-logs:${logCategory}:${viewAccess}:column-visibility`
}

function deserializeLogTypeFilter(value: unknown): unknown[] {
  let values: unknown[] = []
  if (Array.isArray(value)) {
    values = value
  } else if (value) {
    values = [value]
  }
  return values.filter((item) => String(item) !== LOG_TYPE_ALL_VALUE)
}

interface UsageLogsTableProps {
  logCategory: LogCategory
}

export function UsageLogsTable({ logCategory }: UsageLogsTableProps) {
  const { t } = useTranslation()
  const [selectedLog, setSelectedLog] =
    useState<UsageLogDetailSelection | null>(null)
  const isCommon = logCategory === 'common'
  const tableArea = useRef<HTMLDivElement>(null)
  const [layout, setLayout] = useState<'cards' | 'compact' | 'wide'>('wide')
  useLayoutEffect(() => {
    const node = tableArea.current
    if (!isCommon || !node) return
    const update = () => {
      const width = node.clientWidth
      if (!width) return
      if (width < 1250) setLayout('cards')
      else if (width < 1640) setLayout('compact')
      else setLayout('wide')
    }
    update()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(update)
    observer.observe(node)
    return () => observer.disconnect()
  }, [isCommon])
  const getColumnClassName = useCallback(
    (_id: string, kind: 'header' | 'cell') =>
      kind === 'cell' && !isCommon ? 'py-3.5' : undefined,
    [isCommon]
  )
  const {
    isAdminView: isAdmin,
    isRootView: isRoot,
    viewAccess,
    logSource,
  } = useLogsViewScope()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const searchParams = route.useSearch()
  const navigate = route.useNavigate()
  const scrollArea = useRef<HTMLElement | null>(null)
  const previousScrollTop = useRef(0)
  const lastTopWheel = useRef(-Infinity)
  const commonUsage = logCategory === 'common' && logSource === 'usage'
  const preset =
    commonUsage && searchParams.autoRefresh !== false
      ? getLogTimePreset(searchParams)
      : undefined
  const navigateTable: NavigateFn = (options) => {
    const update = options.search
    if (typeof update !== 'function') return void navigate(options)
    void navigate({
      ...options,
      search: (previous) => {
        const next = update(previous)
        if (preset && (previous.page ?? 1) === 1 && Number(next.page) > 1) {
          const { start, end } = getRelativeLogTimeRange(preset)
          return {
            ...next,
            timeRange: preset,
            startTime: start.getTime(),
            endTime: end.getTime(),
          }
        }
        return next
      },
    })
  }
  const user = useAuthStore((state) => state.auth.user)
  const { data: showBillingSource = false } = useQuery({
    queryKey: ['usage-log-billing-source', isAdmin, user?.id],
    enabled: logCategory === 'common' && user?.id != null,
    queryFn: async () => {
      if (isAdmin) {
        const plansResult = await getAdminPlans()
        return shouldShowBillingSource({
          isAdmin,
          plans: plansResult.success ? plansResult.data : undefined,
          subscriptions: undefined,
        })
      }

      const selfResult = await getSelfSubscriptionFull()
      return shouldShowBillingSource({
        isAdmin,
        plans: undefined,
        subscriptions: selfResult.success
          ? selfResult.data?.subscriptions
          : undefined,
      })
    },
  })

  const {
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: navigateTable,
    pagination: { defaultPage: 1, defaultPageSize: isMobile ? 20 : 100 },
    globalFilter: { enabled: false },
    columnFilters: [
      {
        columnId: 'created_at',
        searchKey: 'type',
        type: 'array' as const,
        deserialize: deserializeLogTypeFilter,
      },
      { columnId: 'model_name', searchKey: 'model', type: 'string' as const },
      { columnId: 'token_name', searchKey: 'token', type: 'string' as const },
      { columnId: 'group', searchKey: 'group', type: 'string' as const },
      ...(isAdmin
        ? [
            {
              columnId: 'channel',
              searchKey: 'channel',
              type: 'string' as const,
            },
            {
              columnId: 'username',
              searchKey: 'username',
              type: 'string' as const,
            },
          ]
        : []),
    ],
  })

  const { data, isLoading, isFetching, refetch, isPlaceholderData } = useQuery({
    ...(commonUsage && {
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    }),
    queryKey: [
      'logs',
      logCategory,
      viewAccess,
      logSource,
      user?.id,
      user?.role,
      pagination.pageIndex + 1,
      pagination.pageSize,
      columnFilters,
      searchParams,
      t,
    ],
    queryFn: async () => {
      const result = await fetchLogsByCategory({
        livePreset: preset,
        source: logSource,
        logCategory,
        isAdmin,
        page: pagination.pageIndex + 1,
        pageSize: pagination.pageSize,
        searchParams,
        columnFilters,
      })
      if (!result?.success) {
        throw createServerError(result, t('Failed to load logs'))
      }
      return {
        ...(result.data ?? DEFAULT_LOGS_DATA),
        live: result.live,
        historyFilters: result.historyFilters,
      }
    },
    placeholderData: (previousData, previousQuery) => {
      if (
        previousQuery?.queryKey[1] === logCategory &&
        previousQuery.queryKey[2] === viewAccess &&
        previousQuery.queryKey[3] === logSource &&
        previousQuery.queryKey[4] === user?.id &&
        previousQuery.queryKey[5] === user?.role
      ) {
        return previousData
      }
      return undefined
    },
  })

  const history = useUsageLogHistory(
    commonUsage && !isPlaceholderData ? data?.historyFilters : undefined,
    isAdmin
  )
  const { clearHistory } = history
  const { view, showLatest } = useLiveUsageLogs({
    snapshot: isPlaceholderData ? undefined : data?.live,
    preset,
    pageSize: pagination.pageSize,
    isAdmin,
    refetch,
    isReading: () =>
      history.isBrowsingHistory ||
      pagination.pageIndex > 0 ||
      !!selectedLog ||
      (scrollArea.current?.isConnected && scrollArea.current.scrollTop > 0) ||
      hasOpenLogDialog(),
  })
  const firstPage = view?.items ?? data?.items ?? []
  const total = view?.total ?? data?.total ?? 0
  const loadOlder = () => history.loadOlder(firstPage as UsageLog[], total)
  const logs = history.rows ?? firstPage
  const columns = useColumnsByCategory(
    logCategory,
    isAdmin,
    isRoot,
    setSelectedLog,
    logSource,
    showBillingSource,
    layout === 'compact'
  )
  const isLoadingData = isLoading || (isFetching && !data)

  const { table } = useDataTable({
    data: logs as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    getRowId:
      logCategory === 'common'
        ? (log) => String(log.live_key ?? `${log.request_id ?? ''}:${log.id}`)
        : undefined,
    columnFilters,
    initialColumnVisibility:
      logCategory === 'common' ? commonColumnVisibility : undefined,
    columnVisibilityStorageKey: getColumnVisibilityStorageKey(
      logCategory,
      viewAccess
    ),
    pagination,
    enableRowSelection: false,
    onPaginationChange,
    onColumnFiltersChange,
    manualPagination: true,
    manualFiltering: true,
    totalCount: total,
    ensurePageInRange,
  })

  const refreshFromTop = () => {
    clearHistory()
    if (scrollArea.current) scrollArea.current.scrollTop = 0
    if (pagination.pageIndex > 0) {
      table.setPageIndex(0)
    } else if (view?.pending || view?.reset) {
      showLatest()
    } else if (!isFetching) {
      void refetch()
    }
  }

  const onLogWheel = useEffectEvent((event: WheelEvent) => {
    if (
      event.ctrlKey ||
      Math.abs(event.deltaY) <= Math.abs(event.deltaX) ||
      selectedLog
    ) {
      return
    }
    const viewport =
      event.target instanceof Element
        ? event.target.closest<HTMLElement>(scrollViewport)
        : null
    if (!viewport || !tableArea.current?.contains(viewport)) return
    if (event.deltaY < 0 && viewport.scrollTop <= 0) {
      if (hasOpenLogDialog()) return
      scrollArea.current = viewport
      // Trackpads emit several wheel events after the scrollbar has stopped.
      const now = performance.now()
      const newGesture = now - lastTopWheel.current > 450
      lastTopWheel.current = now
      if (newGesture) refreshFromTop()
    } else if (
      event.deltaY > 0 &&
      atScrollBottom(viewport) &&
      !hasOpenLogDialog()
    ) {
      loadOlder()
    }
  })
  useLayoutEffect(() => {
    const area = tableArea.current
    if (!commonUsage || !area) return
    // Read the position before the browser scrolls. Passive wheel listeners
    // can see zero for the very wheel event that just reached the top.
    area.addEventListener('wheel', onLogWheel, {
      passive: false,
      capture: true,
    })
    return () => area.removeEventListener('wheel', onLogWheel, true)
  }, [commonUsage])

  return (
    <>
      <div
        ref={tableArea}
        className={cn(
          'relative h-full min-h-0',
          commonUsage && '**:data-[slot=data-table-scroll]:overscroll-y-contain'
        )}
        onScrollCapture={(event) => {
          const target = event.target
          if (
            !commonUsage ||
            !(target instanceof HTMLElement) ||
            !target.matches(scrollViewport) ||
            !event.currentTarget.contains(target)
          ) {
            return
          }
          const movingDown =
            target.scrollTop >
            (scrollArea.current === target ? previousScrollTop.current : 0)
          scrollArea.current = target
          previousScrollTop.current = target.scrollTop
          if (
            movingDown &&
            atScrollBottom(target) &&
            !selectedLog &&
            !hasOpenLogDialog()
          ) {
            loadOlder()
          }
        }}
      >
        <DataTablePage
          table={table}
          compactPagination={isCommon && (isMobile || layout === 'cards')}
          mobileLayout={isCommon ? isMobile || layout === 'cards' : undefined}
          columns={columns as ColumnDef<Record<string, unknown>>[]}
          isLoading={isLoadingData}
          isFetching={isFetching}
          emptyTitle={t('No Logs Found')}
          emptyDescription={
            logSource === 'test'
              ? t(
                  'No test logs available. Successful model tests with usage data will appear here.'
                )
              : t(
                  'No usage logs available. Logs will appear here once API calls are made.'
                )
          }
          skeletonKeyPrefix='usage-log-skeleton'
          applyHeaderSize={!isCommon}
          uniformCellText={!isCommon}
          getColumnClassName={getColumnClassName}
          tableClassName={
            isCommon
              ? cn(
                  'rounded-none border-0',
                  styles.grid,
                  layout === 'compact' &&
                    table.getColumn('model_name')?.getIsVisible() &&
                    styles.compact
                )
              : '[&_[data-slot=table]]:text-[13px] [&_[data-slot=table]_td]:text-[13px] [&_[data-slot=table]_td_*]:text-[13px] [&_[data-slot=table]_th]:text-[13px] [&_[data-slot=table]_th_*]:text-[13px]'
          }
          mobile={
            <UsageLogsMobileList
              table={table}
              isLoading={isLoadingData}
              logCategory={logCategory}
            />
          }
          toolbar={
            isCommon ? (
              <CommonLogsFilterBar
                table={table}
                columnFiltersInHeader={
                  commonUsage && !isMobile && layout !== 'cards'
                }
                onLiveTotal={view?.exactTotal}
              />
            ) : (
              <TaskLogsFilterBar table={table} logCategory={logCategory} />
            )
          }
          renderRow={(row) => {
            const logType = (row.original as Record<string, unknown>).type as
              | number
              | undefined
            let tintClass =
              isCommon && logType != null ? (logTypeRowTint[logType] ?? '') : ''
            if (isCommon && isAdmin) {
              const other = getLogMetadata(row.original as UsageLog)
              if (other?.admin_info?.quota_saturation) {
                tintClass = quotaSaturationRowTint
              }
            }

            return (
              <DataTableRow
                key={row.id}
                row={row}
                className={cn('transition-colors', tintClass)}
                getColumnClassName={getColumnClassName}
                cellRenderColumns={table.options.columns}
              />
            )
          }}
        />
        {view?.pending || view?.reset ? (
          <div className='pointer-events-none absolute inset-x-0 bottom-3 z-10 flex justify-center'>
            <Button
              size='sm'
              className='pointer-events-auto shadow-md'
              onClick={refreshFromTop}
            >
              {view.reset || view.pendingUncertain
                ? t('Show latest logs')
                : t('Show new logs ({{count}})', { count: view.pending })}
            </Button>
          </div>
        ) : null}
      </div>
      {selectedLog && (
        <DetailsDialog
          log={selectedLog.log}
          initialView={selectedLog.initialView}
          source={logSource}
          isAdmin={isAdmin}
          isRoot={isRoot}
          open
          onOpenChange={(open) => {
            if (!open) setSelectedLog(null)
          }}
        />
      )}
    </>
  )
}
