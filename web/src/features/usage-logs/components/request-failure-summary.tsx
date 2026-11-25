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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import {
  formatFirstResponseTime,
  getRetryDecisionReason,
  isFailedStreamResponse,
} from '../lib/format'
import { summarizeRequestFailure } from '../lib/request-failure-summary'
import type { LogOtherData } from '../types'
import { DetailRow, LogOverviewCard } from './dialogs/log-detail-layout'

export function RequestFailureSummary(props: {
  metadata?: LogOtherData | null
  error?: string
  showFirstResponse?: boolean
}) {
  const { t, i18n } = useTranslation()
  const metadata = props.metadata
  const firstResponseMs = metadata?.frt
  const failedStream = isFailedStreamResponse(metadata?.stream_status)
  const retryReason = getRetryDecisionReason(metadata?.retry_decision, t)
  if (props.error === undefined && !failedStream && !retryReason) return null
  const originalError =
    metadata?.stream_status?.end_error ||
    props.error ||
    metadata?.stream_status?.errors?.at(-1)
  const failure = summarizeRequestFailure(originalError, metadata)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <LogOverviewCard
      title={t('Request failed')}
      className='ring-destructive/25'
    >
      <div className='space-y-2'>
        <DetailRow label={t('Failure reason')} value={t(failure.label)} />
        {failure.contextLimit !== undefined && (
          <DetailRow
            label={t('Context limit')}
            value={formatNumber(failure.contextLimit, locale)}
            mono
          />
        )}
        {failure.requestedTokens !== undefined && (
          <DetailRow
            label={t('Requested tokens')}
            value={formatNumber(failure.requestedTokens, locale)}
            mono
          />
        )}
        {props.showFirstResponse &&
          firstResponseMs != null &&
          firstResponseMs > 0 && (
            <DetailRow
              label={t('First token')}
              value={formatFirstResponseTime(firstResponseMs)}
            />
          )}
        <DetailRow
          label={
            metadata?.retry_decision?.action === 'stop'
              ? t('Why retry stopped')
              : t('Retry decision')
          }
          value={retryReason ?? t('Not recorded')}
        />
        {originalError && (
          <div className='border-border/60 space-y-1.5 border-t pt-2'>
            <div className='flex items-center justify-between gap-2'>
              <span className='text-muted-foreground text-xs'>
                {t('Original error')}
              </span>
              <CopyButton value={originalError} className='size-7' />
            </div>
            <pre className='max-h-64 overflow-y-auto font-mono text-xs leading-relaxed wrap-anywhere whitespace-pre-wrap'>
              {originalError}
            </pre>
          </div>
        )}
      </div>
    </LogOverviewCard>
  )
}
