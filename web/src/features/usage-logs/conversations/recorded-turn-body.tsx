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
import { skipToken, useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { RequestTraceConversation } from '../components/dialogs/request-trace-conversation'
import { getTurnCapture, type RecordedTurn, type TurnCapture } from './api'
import { recordedTurnSources } from './transcript'

function useRecordedCapture(
  traceId?: string,
  onPartial?: (capture: TurnCapture) => void
) {
  const user = useAuthStore((s) => s.auth.user)
  return useQuery({
    queryKey: [
      'usage-logs',
      'conversation-turn',
      user?.id,
      user?.role,
      traceId,
    ],
    queryFn: traceId
      ? ({ signal }) => getTurnCapture(traceId, signal, onPartial)
      : skipToken,
    enabled: Boolean(traceId && user && user.role >= ROLE.ADMIN),
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    meta: { errorToast: false },
  })
}

export function RecordedTurnBody({
  turn,
  previousTurn,
}: {
  turn: RecordedTurn
  previousTurn?: RecordedTurn
}) {
  const { t } = useTranslation()
  const [partial, setPartial] = useState<TurnCapture>()
  const current = useRecordedCapture(turn.trace_id, setPartial)
  const priorId =
    previousTurn?.conversation_id === turn.conversation_id
      ? previousTurn.trace_id
      : undefined
  const capture =
    current.data ?? (partial?.traceId === turn.trace_id ? partial : undefined)
  // An ID-linked, incremental input already contains only new messages. Download
  // adjacent captured history only when there is replayed context to compare.
  const hasHistory = capture?.sources.some(
    (s) =>
      s.leg.direction.endsWith('_request') &&
      s.messages.some((m) => m.role === 'assistant' || m.role === 'tool')
  )
  const previous = useRecordedCapture(hasHistory ? priorId : undefined)
  const sources = useMemo(() => {
    if (!capture) return []
    return recordedTurnSources(capture, previous.data)
  }, [capture, previous.data])

  if (current.isError) {
    return (
      <ErrorState
        className='min-h-24'
        title={t('Failed to load the request trace')}
        onRetry={() => void current.refetch()}
      />
    )
  }
  if (!capture || (hasHistory && priorId && previous.isPending)) {
    return <LoadingState size='sm' className='min-h-24' />
  }

  const missing =
    current.isSuccess &&
    ['_request', '_response'].some(
      (suffix) =>
        !capture.sources.some(
          (s) => s.leg.direction.endsWith(suffix) && s.messages.length > 0
        )
    )

  return (
    <div className='space-y-2'>
      {previous.isError && (
        <ErrorState
          className='min-h-20'
          title={t('Failed to load the request trace')}
          onRetry={() => void previous.refetch()}
        />
      )}
      {missing && (
        <Alert>
          <AlertDescription>
            {t('Some messages or replies were not captured.')}
          </AlertDescription>
        </Alert>
      )}
      {sources.length ? (
        <RequestTraceConversation legs={[]} sources={sources} inlineTurn />
      ) : (
        <EmptyState
          className='min-h-24'
          title={t('No conversation was captured.')}
        />
      )}
    </div>
  )
}
