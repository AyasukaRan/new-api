import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
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

import { SettingsSwitchField } from '../components/settings-form-layout'
import {
  SOURCE_MATCH_LABELS,
  type RequestSourceCondition,
} from './request-source-rules'

type UnidentifiedGroup = {
  kind: 'signature' | 'no_capture' | 'no_signature'
  conditions: RequestSourceCondition[]
  count: number
  last_seen: number
  current_source: string
}
type UnidentifiedSamples = {
  days: number
  sample_limit: number
  truncated: boolean
  next_cursor: string
  reviewed: number
  groups: UnidentifiedGroup[]
}
type DraftAction = { onCreate?: (conditions: RequestSourceCondition[]) => void }

export function UnidentifiedRequestSources(props: DraftAction) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [days, setDays] = useState(7)
  const [visible, setVisible] = useState(25)
  const [unresolvedOnly, setUnresolvedOnly] = useState(true)
  const client = useQueryClient()
  const queryKey = ['request-source-rules', 'unidentified', days] as const
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: '',
    getNextPageParam: (page: UnidentifiedSamples) =>
      page.next_cursor || undefined,
    queryFn: async ({ signal, pageParam }): Promise<UnidentifiedSamples> => {
      const response = await api.get<{
        success: boolean
        data: UnidentifiedSamples
      }>('/api/option/request_source_rules/unidentified', {
        params: pageParam ? { days, cursor: pageParam } : { days },
        signal,
      })
      return requireServerSuccess(response.data).data
    },
    staleTime: Infinity,
    meta: { errorToast: false },
  })
  const samples = useMemo(() => {
    if (!query.data) return null
    const groups = new Map<string, UnidentifiedGroup>()
    let reviewed = 0
    for (const page of query.data.pages) {
      reviewed += page.reviewed
      for (const group of page.groups) {
        const key = JSON.stringify([
          group.kind,
          group.conditions,
          group.current_source,
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
  const groups = samples?.groups ?? []
  const displayed = unresolvedOnly
    ? groups.filter((group) => !group.current_source)
    : groups
  return (
    <div className='min-w-0 space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <NativeSelect
          size='sm'
          value={days}
          aria-label={t('Time range')}
          onChange={(event) => {
            const nextDays = Number(event.target.value)
            client.removeQueries({
              queryKey: ['request-source-rules', 'unidentified', nextDays],
              type: 'inactive',
            })
            setDays(nextDays)
            setVisible(25)
          }}
        >
          <NativeSelectOption value={1}>
            {t('Last 24 hours')}
          </NativeSelectOption>
          <NativeSelectOption value={7}>{t('Last 7 days')}</NativeSelectOption>
          <NativeSelectOption value={30}>
            {t('Last 30 days')}
          </NativeSelectOption>
        </NativeSelect>
        <SettingsSwitchField
          className='gap-2 border-0 py-0'
          label={t('Only show still-unidentified requests')}
          checked={unresolvedOnly}
          onCheckedChange={(value) => {
            setUnresolvedOnly(value)
            setVisible(25)
          }}
        />
        <Button
          size='sm'
          variant='outline'
          type='button'
          disabled={query.isFetching}
          onClick={() => {
            setVisible(25)
            void client.resetQueries({ queryKey, exact: true })
          }}
        >
          <RefreshCw aria-hidden />
          {t('Refresh')}
        </Button>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Uses the source recorded on the incoming request, or the usage and error logs when it is missing.'
        )}
      </p>
      {query.isPending && <LoadingState />}
      {query.isError && !query.isFetchNextPageError && (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {(!query.isError || query.isFetchNextPageError) &&
        samples &&
        (samples.groups.length === 0 ? (
          <EmptyState
            title={
              query.hasNextPage
                ? t('No unidentified requests in this batch')
                : t('No unidentified requests in this period')
            }
          />
        ) : (
          <>
            <p className='text-muted-foreground text-sm' role='status'>
              {query.data?.pages.length === 1 &&
              query.data.pages[0].truncated &&
              samples.reviewed === query.data.pages[0].sample_limit
                ? t(
                    'Showing the latest {{limit}} unidentified requests in this period.',
                    {
                      limit: formatNumber(
                        query.data.pages[0].sample_limit,
                        locale
                      ),
                    }
                  )
                : t(
                    'Reviewed {{total}} unidentified requests in this period.',
                    { total: formatNumber(samples.reviewed, locale) }
                  )}
            </p>
            <StaticDataTable
              data={displayed.slice(0, visible)}
              emptyContent={t('All reviewed requests now match a source.')}
              columns={[
                {
                  id: 'signature',
                  header: t('Client signature'),
                  cell: (row) => {
                    if (row.kind === 'no_capture') {
                      return t('No captured request headers')
                    }
                    if (row.kind === 'no_signature') {
                      return t('No distinctive client headers')
                    }
                    return (
                      <ul className='space-y-1 whitespace-normal'>
                        {row.conditions.map((c) => (
                          <li key={c.header + c.match + c.value}>
                            <code className='text-xs break-all'>
                              {c.header}
                            </code>{' '}
                            <span className='text-muted-foreground text-xs'>
                              {t(SOURCE_MATCH_LABELS[c.match])}
                            </span>{' '}
                            <code className='text-xs break-all'>{c.value}</code>
                          </li>
                        ))}
                      </ul>
                    )
                  },
                },
                {
                  id: 'requests',
                  header: t('Requests'),
                  cell: (row) => formatNumber(row.count, locale),
                },
                {
                  id: 'last_seen',
                  header: t('Last Seen'),
                  cell: (row) => formatTimestampToDate(row.last_seen),
                },
                {
                  id: 'current_source',
                  header: t('Current recognition'),
                  cell: (row) => row.current_source || t('Unidentified source'),
                },
                {
                  id: 'create',
                  header: t('Actions'),
                  cell: (row) =>
                    row.kind === 'signature' &&
                    row.conditions.length > 0 && (
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        disabled={!props.onCreate}
                        onClick={() => props.onCreate?.(row.conditions)}
                      >
                        {t('Create rule')}
                      </Button>
                    ),
                },
              ]}
            />
            {visible < displayed.length && (
              <Button
                type='button'
                size='sm'
                variant='outline'
                onClick={() => setVisible(visible + 25)}
              >
                {t('Show more ({{remaining}} remaining)', {
                  remaining: formatNumber(displayed.length - visible, locale),
                })}
              </Button>
            )}
          </>
        ))}
      {query.isFetchNextPageError && (
        <p className='text-destructive text-sm' role='alert'>
          {getServerErrorMessage(query.error)}
        </p>
      )}
      {query.hasNextPage && (
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={query.isFetching}
          onClick={() => void query.fetchNextPage()}
        >
          {t('Review older requests')}
        </Button>
      )}
    </div>
  )
}
