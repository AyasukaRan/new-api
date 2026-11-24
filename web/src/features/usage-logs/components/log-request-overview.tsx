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

import { LOG_TYPE_ENUM } from '../constants'
import type { UsageLog } from '../data/schema'
import { getLogTokenTotals } from '../lib/cache-usage'
import { isViolationFeeLog, renderLogContent } from '../lib/format'
import { isConsumeLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { LogOverviewCard } from './dialogs/log-detail-layout'
import { RequestTraceSection } from './dialogs/request-trace-section'
import { LogChargeDetails } from './log-charge-details'
import {
  LogRequestIdentifiers,
  LogRequestInfoCards,
} from './log-request-info-cards'
import { LogTokenBreakdown } from './log-token-breakdown'
import { RequestFailureSummary } from './request-failure-summary'

export function LogRequestOverview(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const { log, other } = props
  const traceId = props.isAdmin && other?.admin_info?.trace_id
  const { hasUsage: hasTokens } = getLogTokenTotals(log, other)
  const hasCharge = isConsumeLogType(log.type) || log.quota > 0

  return (
    <div data-slot='log-request-overview' className='space-y-3'>
      <RequestFailureSummary
        metadata={other}
        error={
          log.type === LOG_TYPE_ENUM.ERROR
            ? (renderLogContent(other, t) ?? log.content)
            : undefined
        }
      />
      <div className='grid min-w-0 items-stretch gap-3 md:grid-cols-2'>
        <LogRequestInfoCards {...props} />
        {hasTokens && (
          <LogOverviewCard
            title={t('Token Breakdown')}
            className={hasCharge ? undefined : 'md:col-span-2'}
          >
            <LogTokenBreakdown log={log} other={other} variant='detail' />
          </LogOverviewCard>
        )}
        {hasCharge && (
          <LogOverviewCard
            title={t('Billing Details')}
            className={hasTokens ? undefined : 'md:col-span-2'}
          >
            <LogChargeDetails
              compact
              log={log}
              other={other}
              isAdmin={
                props.isAdmin &&
                log.type === LOG_TYPE_ENUM.CONSUME &&
                !isViolationFeeLog(other)
              }
            />
          </LogOverviewCard>
        )}
        <LogRequestIdentifiers log={log} isAdmin={props.isAdmin} />
      </div>
      {traceId && (
        <RequestTraceSection traceId={traceId} enabled metadata={other} />
      )}
    </div>
  )
}
