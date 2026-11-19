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
import { ArrowLeft, MessagesSquare, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestamp } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  getConversationRecords,
  getRecordedTurns,
  type ConversationRecord,
} from './api'
import { ConversationTimeline } from './timeline'

function ConversationReader({
  record,
  viewer,
  onBack,
}: {
  record: ConversationRecord
  viewer: number
  onBack: () => void
}) {
  const { t } = useTranslation()
  const query = useInfiniteQuery({
    queryKey: ['saved-conversations', viewer, 'turns', record.conversation_id],
    queryFn: ({ pageParam, signal }) =>
      getRecordedTurns(record.conversation_id, pageParam, signal),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const turns = useMemo(() => {
    const seen = new Set<string>()
    return (
      query.data?.pages.flatMap((page) =>
        page.items.filter((turn) => {
          if (seen.has(turn.trace_id)) return false
          seen.add(turn.trace_id)
          return true
        })
      ) ?? []
    )
  }, [query.data])

  return (
    <section className='flex h-full min-h-0 min-w-0 flex-col'>
      <div className='flex shrink-0 items-center gap-2 border-b px-3 py-3 sm:px-5'>
        <Button
          variant='ghost'
          size='icon-sm'
          className='md:hidden'
          aria-label={t('Back to conversations')}
          onClick={onBack}
        >
          <ArrowLeft />
        </Button>
        <div className='min-w-0'>
          <h3 className='truncate text-sm font-semibold' title={record.title}>
            {record.title || t('Untitled conversation')}
          </h3>
          <p className='text-muted-foreground truncate text-xs'>
            {[record.model_name, formatTimestamp(record.updated_at)]
              .filter(Boolean)
              .join(' · ')}
          </p>
        </div>
      </div>
      <div className='min-h-0 flex-1 overflow-auto overscroll-contain p-3 sm:p-5'>
        {query.isPending && <LoadingState />}
        {query.isError && (
          <ErrorState
            className='min-h-24'
            title={t('Failed to load conversations')}
            onRetry={() =>
              void (query.isFetchNextPageError
                ? query.fetchNextPage()
                : query.refetch())
            }
          />
        )}
        {turns.length > 0 && (
          <ConversationTimeline
            turns={turns}
            conversationId={record.conversation_id}
          />
        )}
        {query.hasNextPage && !query.isFetchNextPageError && (
          <Button
            variant='outline'
            size='sm'
            className='mt-4'
            disabled={query.isFetching}
            onClick={() => void query.fetchNextPage()}
          >
            {t('Load earlier turns')}
          </Button>
        )}
        {query.isSuccess && turns.length === 0 && (
          <EmptyState title={t('No conversation was captured.')} />
        )}
      </div>
    </section>
  )
}

export function ConversationRecordsPage() {
  const { t, i18n } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user)
  const client = useQueryClient()
  const [selected, setSelected] = useState<ConversationRecord>()
  const query = useInfiniteQuery({
    queryKey: ['saved-conversations', viewer?.id, 'list'],
    queryFn: ({ pageParam, signal }) =>
      getConversationRecords(pageParam, signal),
    enabled: !!viewer && viewer.role >= ROLE.ADMIN,
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
    meta: { errorToast: false },
  })
  const records = useMemo(() => {
    const seen = new Set<string>()
    return (
      query.data?.pages.flatMap((page) =>
        page.items.filter((record) => {
          if (seen.has(record.conversation_id)) return false
          seen.add(record.conversation_id)
          return true
        })
      ) ?? []
    )
  }, [query.data])
  if (!viewer || viewer.role < ROLE.ADMIN) return null
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const active =
    records.find((row) => row.conversation_id === selected?.conversation_id) ??
    selected

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Conversation Records')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          disabled={query.isFetching}
          onClick={() =>
            void client.invalidateQueries({
              queryKey: ['saved-conversations', viewer.id],
            })
          }
        >
          <RefreshCw />
          {t('Refresh')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='bg-card grid h-full min-h-0 grid-cols-1 overflow-hidden rounded-xl border md:grid-cols-[minmax(14rem,19rem)_minmax(0,1fr)]'>
          <nav
            aria-label={t('Conversation Records')}
            className={cn(
              'min-h-0 overflow-y-auto p-2 md:border-r',
              active && 'max-md:hidden'
            )}
          >
            {query.isPending && <LoadingState />}
            {query.isError && (
              <ErrorState
                className='min-h-28'
                title={t('Failed to load conversations')}
                onRetry={() =>
                  void (query.isFetchNextPageError
                    ? query.fetchNextPage()
                    : query.refetch())
                }
              />
            )}
            {records.map((record) => {
              const chosen = record.conversation_id === active?.conversation_id
              return (
                <Button
                  key={record.conversation_id}
                  variant={chosen ? 'secondary' : 'ghost'}
                  aria-current={chosen ? 'true' : undefined}
                  onClick={() => setSelected(record)}
                  className='h-auto w-full flex-col items-start justify-start gap-1 px-3 py-2.5 text-left'
                >
                  <span className='w-full truncate font-medium'>
                    {record.title || t('Untitled conversation')}
                  </span>
                  <span className='text-muted-foreground w-full truncate text-xs font-normal'>
                    {record.model_name} · {t('User ID')} {record.user_id}
                  </span>
                  <span className='text-muted-foreground flex w-full flex-wrap justify-between gap-x-2 text-xs font-normal'>
                    <span>{formatTimestamp(record.updated_at)}</span>
                    <span>
                      {t('Recorded turns: {{total}}', {
                        total: formatNumber(record.turn_count, locale),
                      })}
                    </span>
                  </span>
                </Button>
              )
            })}
            {query.isSuccess && !records.length && (
              <EmptyState
                icon={MessagesSquare}
                className='min-h-32'
                title={t('No saved conversations')}
              />
            )}
            {query.hasNextPage && !query.isFetchNextPageError && (
              <Button
                className='mt-2 w-full'
                size='sm'
                variant='outline'
                disabled={query.isFetching}
                onClick={() => void query.fetchNextPage()}
              >
                {t('Load more conversations')}
              </Button>
            )}
          </nav>
          {active ? (
            <ConversationReader
              key={active.conversation_id}
              record={active}
              viewer={viewer.id}
              onBack={() => setSelected(undefined)}
            />
          ) : (
            <div className='hidden place-items-center md:grid'>
              <EmptyState
                icon={MessagesSquare}
                title={t('Select a conversation')}
              />
            </div>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
