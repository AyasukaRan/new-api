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

import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { useTracePayload } from '../../hooks/use-trace-payload'
import {
  conversationSource,
  pairConversationTools,
  selectConversationLegs,
} from '../../lib/request-trace-conversation'
import type { RequestTraceLeg } from '../../types'
import { RequestTraceConversation } from './request-trace-conversation'

export function RequestTraceConversationLoader(props: {
  traceId: string
  legs: RequestTraceLeg[]
}) {
  const { t, i18n } = useTranslation()
  const selected = useMemo(
    () => selectConversationLegs(props.legs),
    [props.legs]
  )
  const input = selected.find((leg) => leg.direction.endsWith('_request'))
  const output = selected.find((leg) => leg.direction.endsWith('_response'))
  const inputQuery = useTracePayload(props.traceId, input, 'conversation')
  const outputQuery = useTracePayload(props.traceId, output, 'rendered')
  const inputSource = inputQuery.isError
    ? undefined
    : (inputQuery.data?.source ?? inputQuery.progress?.source)
  const outputSource = outputQuery.isError
    ? undefined
    : (outputQuery.data?.source ?? outputQuery.progress?.source)
  const sources = useMemo(
    () =>
      pairConversationTools(
        selected.flatMap((leg) => {
          if (!leg.has_body) return [conversationSource(leg)]
          const source = leg.direction.endsWith('_request')
            ? inputSource
            : outputSource
          return source ? [source] : []
        })
      ),
    [selected, inputSource, outputSource]
  )
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  if (!selected.some((leg) => leg.has_body)) {
    return (
      <div className='h-full overflow-y-auto'>
        <RequestTraceConversation legs={props.legs} />
      </div>
    )
  }
  return (
    <div className='flex h-full min-h-0 flex-col gap-4'>
      {[
        { leg: input, query: inputQuery },
        { leg: output, query: outputQuery },
      ].map(({ leg: currentLeg, query: currentQuery }) => {
        if (!currentLeg?.has_body) return null
        const label = currentLeg.direction.endsWith('_request')
          ? t('Input history')
          : t('Final response')
        if (currentQuery.isError) {
          return (
            <ErrorState
              key={currentLeg.seq}
              className='min-h-24'
              title={`${label}: ${t('Failed to load the request trace')}`}
              onRetry={() => void currentQuery.refetch()}
            />
          )
        }
        if (!currentQuery.isPending) return null
        return (
          <div
            key={currentLeg.seq}
            role='status'
            aria-label={label}
            className='mx-auto w-full max-w-4xl shrink-0'
          >
            <LoadingState
              inline
              size='sm'
              message={t('Loading {{label}} · {{bytes}} bytes', {
                label,
                bytes: formatNumber(currentQuery.progress?.bytes ?? 0, locale),
              })}
            />
          </div>
        )
      })}
      {sources.length > 0 && (
        <div className='min-h-0 flex-1'>
          <RequestTraceConversation legs={props.legs} sources={sources} />
        </div>
      )}
    </div>
  )
}
