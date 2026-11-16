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

import {
  formatFirstResponseTime,
  getRetryDecisionReason,
  isFailedStreamResponse,
} from '../lib/format'
import type { LogOtherData } from '../types'
import { DetailRow, DetailSection } from './dialogs/log-detail-layout'

export function RequestFailureSummary(props: {
  metadata?: LogOtherData | null
  error?: string
  showFirstResponse?: boolean
}) {
  const { t } = useTranslation()
  const metadata = props.metadata
  const firstResponseMs = metadata?.frt
  const failedStream = isFailedStreamResponse(metadata?.stream_status)
  const retryReason = getRetryDecisionReason(metadata?.retry_decision, t)
  if (props.error === undefined && !failedStream && !retryReason) return null
  const failureReason =
    metadata?.stream_status?.end_error ||
    props.error ||
    metadata?.stream_status?.errors?.at(-1) ||
    t('Request failed')
  return (
    <DetailSection label={t('Request failed')} variant='danger'>
      <DetailRow label={t('Failure reason')} value={failureReason} />
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
    </DetailSection>
  )
}
