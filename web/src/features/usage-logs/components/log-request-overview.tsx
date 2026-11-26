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
import { canShowLogCost, renderLogContent } from '../lib/format'
import { isConsumeLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { LogOverviewCard } from './dialogs/log-detail-layout'
import { LogChargeDetails } from './log-charge-details'
import { LogInputAnalysis } from './log-input-analysis'
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
  showFailure: boolean
}) {
  const { t } = useTranslation()
  const { log, other } = props
  const { hasUsage: hasTokens } = getLogTokenTotals(log, other)
  const showCost = props.isAdmin && canShowLogCost(log.type, other)
  const hasCharge = isConsumeLogType(log.type) || log.quota > 0 || showCost
  const admin = props.isAdmin ? other?.admin_info : undefined

  return (
    <div data-slot='log-request-overview' className='space-y-3'>
      {props.showFailure && (
        <RequestFailureSummary
          metadata={other}
          error={
            log.type === LOG_TYPE_ENUM.ERROR
              ? (renderLogContent(other, t) ?? log.content)
              : undefined
          }
        />
      )}
      <div className='grid min-w-0 items-stretch gap-3 md:grid-cols-2'>
        <LogRequestInfoCards {...props} />
        {(hasTokens ||
          admin?.trace_id ||
          admin?.request_body_ref ||
          admin?.request_body) && (
          <LogOverviewCard
            title={hasTokens ? t('Token Breakdown') : t('Input')}
            className={hasCharge ? undefined : 'md:col-span-2'}
          >
            {hasTokens && (
              <LogTokenBreakdown log={log} other={other} variant='detail' />
            )}
            {props.isAdmin && (
              <div className={hasTokens ? 'mt-3 border-t pt-1' : undefined}>
                <LogInputAnalysis
                  log={log}
                  admin={admin ?? {}}
                  path={other?.request_path}
                />
              </div>
            )}
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
              isAdmin={showCost}
            />
          </LogOverviewCard>
        )}
        <LogRequestIdentifiers log={log} isAdmin={props.isAdmin} />
      </div>
    </div>
  )
}
