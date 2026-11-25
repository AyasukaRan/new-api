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
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { SettingsSwitchField } from '../components/settings-form-layout'

type ErrorGroup = {
  pattern: string
  suggested_pattern?: string
  suggested_match?: 'contains'
  status_code?: number
  count: number
  last_seen: number
  label: string
  matched: boolean
  rule_id?: string
}
type ErrorInventory = {
  groups: ErrorGroup[]
  reviewed: number
  next_cursor: string
}

export function ErrorHistoryInventory(props: {
  onCreate?: (pattern: string, statusCode?: number) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const user = useAuthStore((state) => state.auth.user)
  const [days, setDays] = useState(0)
  const [unmatchedOnly, setUnmatchedOnly] = useState(false)
  const [autoScan, setAutoScan] = useState(false)
  const [visible, setVisible] = useState(25)
  const client = useQueryClient()
  const queryKey = [
    'error-recognition',
    'inventory',
    user?.id,
    user?.role,
    days,
  ]
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: '',
    getNextPageParam: (page: ErrorInventory) => page.next_cursor || undefined,
    queryFn: async ({ signal, pageParam }): Promise<ErrorInventory> => {
      const response = await api.get<{
        success: boolean
        data: ErrorInventory
      }>('/api/option/error_recognition_rules/inventory', {
        params: { days, ...(pageParam ? { cursor: pageParam } : {}) },
        signal,
      })
      return requireServerSuccess(response.data).data
    },
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const { hasNextPage, isFetching, isError, fetchNextPage } = query
  useEffect(() => {
    if (autoScan && hasNextPage && !isFetching && !isError) {
      void fetchNextPage()
    }
  }, [autoScan, hasNextPage, isFetching, isError, fetchNextPage])
  const samples = useMemo(() => {
    const groups = new Map<string, ErrorGroup>()
    let reviewed = 0
    for (const page of query.data?.pages ?? []) {
      reviewed += page.reviewed
      for (const group of page.groups) {
        const key = JSON.stringify([
          group.pattern,
          group.status_code,
          group.rule_id,
          group.matched,
        ])
        const previous = groups.get(key)
        if (previous) {
          previous.count += group.count
          previous.last_seen = Math.max(previous.last_seen, group.last_seen)
        } else {
          groups.set(key, { ...group })
        }
      }
    }
    return {
      reviewed,
      groups: [...groups.values()].sort(
        (a, b) => b.count - a.count || b.last_seen - a.last_seen
      ),
    }
  }, [query.data])
  const groups = unmatchedOnly
    ? samples.groups.filter((item) => !item.matched)
    : samples.groups
  return (
    <div className='min-w-0 space-y-4'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Historical errors are grouped using current rules. Original log content is unchanged. Counts include failed attempts and interrupted responses.'
        )}
      </p>
      <div className='flex flex-wrap items-center gap-3'>
        <NativeSelect
          size='sm'
          aria-label={t('Time range')}
          value={days}
          onChange={(event) => {
            setDays(Number(event.target.value))
            setVisible(25)
            setAutoScan(false)
          }}
        >
          <NativeSelectOption value={0}>
            {t('All retained history')}
          </NativeSelectOption>
          <NativeSelectOption value={1}>
            {t('Last 24 hours')}
          </NativeSelectOption>
          <NativeSelectOption value={7}>{t('Last 7 days')}</NativeSelectOption>
          <NativeSelectOption value={30}>
            {t('Last 30 days')}
          </NativeSelectOption>
        </NativeSelect>
        <SettingsSwitchField
          className='gap-2 py-0'
          label={t('Unmatched errors only')}
          checked={unmatchedOnly}
          onCheckedChange={(value) => {
            setUnmatchedOnly(value)
            setVisible(25)
          }}
        />
        <Button
          variant='outline'
          size='sm'
          type='button'
          disabled={query.isFetching}
          onClick={() => {
            setAutoScan(false)
            setVisible(25)
            void client.resetQueries({ queryKey, exact: true })
          }}
        >
          {t('Refresh')}
        </Button>
        {query.hasNextPage && (
          <Button
            variant='outline'
            size='sm'
            type='button'
            onClick={() => {
              if (autoScan) setAutoScan(false)
              else {
                setAutoScan(true)
                if (query.isError) void query.fetchNextPage()
              }
            }}
          >
            {autoScan ? t('Pause scan') : t('Scan remaining history')}
          </Button>
        )}
      </div>
      <p className='text-muted-foreground text-xs' role='status'>
        {t('Scanned {{count}} records', {
          count: formatNumber(samples.reviewed, locale),
        })}
        {query.isSuccess && !query.hasNextPage && ` · ${t('Scan complete')}`}
      </p>
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => {
            if (query.isFetchNextPageError) void query.fetchNextPage()
            else void query.refetch()
          }}
        />
      )}
      {query.data && (
        <StaticDataTable
          tableClassName='max-sm:min-w-3xl'
          data={groups.slice(0, visible)}
          emptyContent={t('No matching errors')}
          columns={[
            {
              id: 'pattern',
              header: t('Error pattern'),
              className: 'max-sm:min-w-72',
              cell: (row) => (
                <code className='block max-w-xl text-xs wrap-anywhere whitespace-pre-wrap'>
                  {row.pattern}
                </code>
              ),
            },
            {
              id: 'status',
              header: t('Status Code'),
              cell: (row) => row.status_code || '—',
            },
            {
              id: 'count',
              header: t('Count'),
              cell: (row) => formatNumber(row.count, locale),
            },
            {
              id: 'label',
              header: t('Current recognition'),
              cell: (row) => (row.matched ? t(row.label) : t('Unmatched')),
            },
            {
              id: 'time',
              header: t('Last Seen'),
              cell: (row) => formatTimestampToDate(row.last_seen),
            },
            {
              id: 'action',
              header: t('Actions'),
              cell: (row) => (
                <Button
                  type='button'
                  size='sm'
                  variant='outline'
                  disabled={!props.onCreate}
                  onClick={() =>
                    props.onCreate?.(
                      row.suggested_pattern ?? '',
                      row.status_code
                    )
                  }
                >
                  {t('Create rule')}
                </Button>
              ),
            },
          ]}
        />
      )}
      {visible < groups.length && (
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setVisible(visible + 25)}
        >
          {t('Show more ({{remaining}} remaining)', {
            remaining: formatNumber(groups.length - visible, locale),
          })}
        </Button>
      )}
    </div>
  )
}
