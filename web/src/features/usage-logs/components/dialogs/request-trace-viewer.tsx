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
import { MessageSquare, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsAdmin } from '@/hooks/use-admin'
import { useAuthStore } from '@/stores/auth-store'

import { getRequestTrace } from '../../api'
import type { LogOtherData } from '../../types'
import { RequestMetadataTags } from '../request-metadata-tags'
import { RequestTraceConversation } from './request-trace-conversation'
import { RequestTraceExchange } from './request-trace-exchange'

export default function RequestTraceViewer(props: {
  traceId: string
  metadata?: LogOtherData | null
}) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const traceQuery = useQuery({
    queryKey: [
      'usage-logs',
      'request-trace',
      user?.id,
      user?.role,
      props.traceId,
    ],
    queryFn: ({ signal }) => getRequestTrace(props.traceId, signal),
    enabled: isAdmin && user != null,
    retry: false,
    staleTime: 60_000,
    gcTime: 0,
    placeholderData: undefined,
  })

  if (!isAdmin) return null

  const traceIdentity = (
    <div className='text-muted-foreground flex min-w-0 items-center gap-2 text-xs'>
      <span className='max-w-64 truncate font-mono' title={props.traceId}>
        {props.traceId}
      </span>
      <CopyButton
        value={props.traceId}
        className='size-7'
        aria-label={t('Copy trace ID')}
      />
    </div>
  )

  if (traceQuery.isPending) {
    return (
      <div role='status' aria-label={t('Loading...')}>
        <LoadingState />
      </div>
    )
  }

  if (traceQuery.isError) {
    return (
      <ErrorState
        title={t('Failed to load the request trace')}
        onRetry={() => void traceQuery.refetch()}
        action={traceIdentity}
      />
    )
  }

  const legs = traceQuery.data
  if (legs.length === 0) {
    return (
      <EmptyState
        icon={Route}
        title={t('No trace records are available.')}
        action={
          <>
            <Button
              variant='outline'
              size='sm'
              disabled={traceQuery.isFetching}
              onClick={() => void traceQuery.refetch()}
            >
              {t('Refresh')}
            </Button>
            {traceIdentity}
          </>
        }
      />
    )
  }

  const attempts = new Set(
    legs
      .filter((leg) => leg.direction.startsWith('upstream_'))
      .map((leg) => leg.attempt)
  ).size

  return (
    <Tabs defaultValue='conversation' className='h-full min-h-0 gap-0'>
      <div className='flex shrink-0 flex-wrap items-center justify-between gap-2 border-b pb-3'>
        <TabsList aria-label={t('Request trace views')}>
          <TabsTrigger value='conversation'>
            <MessageSquare aria-hidden='true' />
            {t('Conversation')}
          </TabsTrigger>
          <TabsTrigger value='exchange'>
            <Route aria-hidden='true' />
            {t('Exchange details')}
          </TabsTrigger>
        </TabsList>
        <div className='text-muted-foreground flex min-w-0 items-center gap-2 text-xs'>
          {attempts > 0 && (
            <span>{t('Attempts: {{count}}', { count: attempts })}</span>
          )}
          {traceIdentity}
        </div>
      </div>
      <div className='shrink-0 pt-2'>
        <RequestMetadataTags metadata={props.metadata} />
      </div>
      <TabsContent
        value='conversation'
        className='min-h-0 overflow-y-auto overscroll-contain pt-4'
      >
        <RequestTraceConversation legs={legs} />
      </TabsContent>
      <TabsContent value='exchange' className='min-h-0 overflow-hidden pt-3'>
        <RequestTraceExchange traceId={props.traceId} legs={legs} />
      </TabsContent>
    </Tabs>
  )
}
