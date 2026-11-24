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
import { Globe, ShieldCheck, UserCog, Info, LogIn } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { formatTimestampToDate, formatUseTime } from '@/lib/format'
import { translateServerText } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { AuditDetailFields } from '../../audit/components/audit-detail-fields'
import type { UsageLog } from '../../data/schema'
import {
  parseLogOther,
  formatFirstResponseTime,
  getFirstResponseTimeColor,
  getResponseTimeColor,
  logTokenName,
  renderAuditContent,
  renderLogContent,
} from '../../lib/format'
import { buildQuotaAuditOperation } from '../../lib/quota-audit-operation'
import { getLogTypeConfig, isTimingLogType } from '../../lib/utils'
import type {
  LogOtherData,
  LogSource,
  UsageLogDetailSelection,
} from '../../types'
import { LogBillingEvidence } from '../log-billing-evidence'
import { LogRequestDetailTabs } from '../log-metrics-detail-dialog'
import { RequestMetadataTags } from '../request-metadata-tags'
import { DetailRow, DetailSection } from './log-detail-layout'
import { LogRequestDiagnostics } from './log-request-diagnostics'
import { RequestTraceSection } from './request-trace-section'

// Maps a channel-update changed-field token (as recorded by the backend audit)
// to its i18n label key for display in the audit details.
const CHANNEL_FIELD_LABELS: Record<string, string> = {
  status: 'Status',
  models: 'Models',
  group: 'Group',
  type: 'Type',
  base_url: 'Base URL',
  key: 'Key',
}

function timingTextColorClass(
  variant: 'success' | 'warning' | 'danger'
): string {
  if (variant === 'success') return 'text-emerald-600'
  if (variant === 'warning') return 'text-amber-600'
  return 'text-rose-600'
}

interface DetailsDialogProps {
  initialView?: UsageLogDetailSelection['initialView']
  source?: LogSource
  log: UsageLog
  isAdmin: boolean
  isRoot: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}

function LegacyLogDetails(
  props: DetailsDialogProps & { other: LogOtherData | null }
) {
  const { t } = useTranslation()
  const other = props.other
  const createdAt = formatTimestampToDate(props.log.created_at)

  const isRefund = props.log.type === 6
  const isTopup = props.log.type === 1
  const isManage = props.log.type === 3
  const showTiming = isTimingLogType(props.log.type)
  const showAdminIp =
    !!props.log.ip && (showTiming || (props.isAdmin && isTopup))
  const adminInfo = other?.admin_info
  const topupAuditFields =
    isTopup && props.isAdmin && adminInfo
      ? ([
          adminInfo.payment_method && {
            label: t('Order Payment Method'),
            value: adminInfo.payment_method,
          },
          adminInfo.callback_payment_method && {
            label: t('Callback Payment Method'),
            value: adminInfo.callback_payment_method,
          },
          adminInfo.caller_ip && {
            label: t('Callback Caller IP'),
            value: adminInfo.caller_ip,
          },
          adminInfo.server_ip && {
            label: t('Server IP'),
            value: adminInfo.server_ip,
          },
          adminInfo.node_name && {
            label: t('Node Name'),
            value: adminInfo.node_name,
          },
          adminInfo.version && {
            label: t('System Version'),
            value: adminInfo.version,
          },
        ].filter(Boolean) as Array<{ label: string; value: string }>)
      : []
  const showLegacyTopupWarning = isTopup && props.isAdmin && !adminInfo
  const showTopupAuditSection =
    isTopup &&
    props.isAdmin &&
    (topupAuditFields.length > 0 || showLegacyTopupWarning)
  const manageOperator = (() => {
    if (!isManage || !props.isAdmin || !adminInfo) return null
    const username = adminInfo.admin_username
    const id = adminInfo.admin_id
    const hasUsername = username != null && String(username).trim() !== ''
    const hasId = id != null && String(id).trim() !== ''
    if (!hasUsername && !hasId) return null
    if (hasUsername && hasId) return `${username} (ID: ${id})`
    if (hasUsername) return String(username)
    return `ID: ${id}`
  })()
  const authMethodLabel = (() => {
    if (!isManage || !props.isAdmin || !adminInfo?.auth_method) return ''
    if (adminInfo.auth_method === 'access_token') return t('Access Token')
    if (adminInfo.auth_method === 'session') return t('Session')
    return String(adminInfo.auth_method)
  })()

  // Top-up, audit, and login logs share the language-independent descriptor.
  const quotaOperation = isTopup
    ? buildQuotaAuditOperation(
        other?.op?.action ?? '',
        other?.op?.params ?? {},
        true,
        t
      )
    : null
  const operationText = renderAuditContent(other, t)
  const details =
    (isTopup ? operationText : null) ??
    renderLogContent(other, t) ??
    props.log.content ??
    ''
  const auditRoute = isManage && props.isAdmin ? other?.audit_info : undefined
  // Channel update records which fields changed (stable field tokens); render
  // them with their localized labels for admins.
  const changedFieldTokens =
    isManage &&
    props.isAdmin &&
    Array.isArray(other?.op?.params?.changed_fields)
      ? (other.op.params.changed_fields as string[])
      : []
  const changedFieldsText = changedFieldTokens
    .map((field) => t(CHANNEL_FIELD_LABELS[field] ?? field))
    .join(', ')
  const showManageAuditSection =
    isManage && props.isAdmin && (operationText != null || auditRoute != null)

  // Administrators can also inspect their own historical login records.
  const isLogin = props.log.type === 7
  const loginAuditFields = isLogin
    ? ([
        other?.login_method && {
          label: t('Login Method'),
          value: String(other.login_method),
        },
        props.log.ip && {
          label: t('IP Address'),
          value: props.log.ip,
        },
        other?.user_agent && {
          label: t('User Agent'),
          value: String(other.user_agent),
        },
      ].filter(Boolean) as Array<{ label: string; value: string }>)
    : []

  const useChannel = other?.admin_info?.use_channel
  const channelChain =
    useChannel && useChannel.length > 0 ? useChannel.join(' → ') : undefined

  return (
    <section aria-label={t('Log Details')}>
      <div className='w-full max-w-full min-w-0 space-y-2.5 overflow-x-hidden py-1 sm:space-y-3'>
        <RequestMetadataTags
          isAdmin={props.isAdmin}
          metadata={other}
          expanded
        />

        {/* Full request/response exchange (admin only) */}
        {props.isAdmin && adminInfo?.trace_id && (
          <RequestTraceSection
            traceId={adminInfo.trace_id}
            enabled={props.open}
            metadata={other}
          />
        )}

        {/* Overview section - key identifiers */}
        <div className='min-w-0 space-y-1'>
          {props.log.created_at > 0 && (
            <DetailRow
              label={t('Time')}
              value={
                <span className='inline-flex items-center gap-1.5 tabular-nums'>
                  {createdAt}
                  <CopyButton
                    value={createdAt}
                    aria-label={`${t('Copy to clipboard')} · ${t('Time')}`}
                    className='size-6'
                    iconClassName='size-3'
                  />
                </span>
              }
              mono
            />
          )}
          {props.log.request_id && (
            <DetailRow
              label={t('Request ID')}
              value={props.log.request_id}
              mono
            />
          )}
          {props.isAdmin && props.log.upstream_request_id && (
            <DetailRow
              label={t('Upstream Request ID')}
              value={props.log.upstream_request_id}
              mono
            />
          )}

          {props.isAdmin && props.log.channel > 0 && (
            <DetailRow
              label={t('Channel')}
              value={
                <span>
                  {props.log.channel}
                  {props.log.channel_name && (
                    <span className='text-muted-foreground'>
                      {' '}
                      ({props.log.channel_name})
                    </span>
                  )}
                </span>
              }
              mono
            />
          )}

          {channelChain && props.isAdmin && (
            <DetailRow label={t('Retry Chain')} value={channelChain} mono />
          )}

          {props.log.token_name && (
            <DetailRow
              label={t('Token')}
              value={logTokenName(props.log, t)}
              mono
            />
          )}

          {(props.log.group || other?.group) && (
            <DetailRow
              label={t('Group')}
              value={props.log.group || other?.group || ''}
              mono
            />
          )}

          {showAdminIp && (
            <DetailRow
              label={t('IP Address')}
              value={
                <span className='flex items-center gap-1'>
                  <Globe className='size-3 text-amber-500' aria-hidden='true' />
                  {props.log.ip}
                </span>
              }
              mono
            />
          )}

          {showTiming &&
            (props.log.use_time > 0 ||
              (props.log.is_stream && (other?.frt ?? 0) > 0)) && (
              <DetailRow
                label={t('Response Time')}
                value={
                  <span
                    className={cn(
                      'font-medium',
                      timingTextColorClass(
                        getResponseTimeColor(
                          props.log.use_time,
                          props.log.completion_tokens
                        )
                      )
                    )}
                  >
                    {formatUseTime(props.log.use_time)}
                    {props.log.is_stream &&
                      other?.frt != null &&
                      other.frt > 0 && (
                        <span
                          className={cn(
                            'font-normal',
                            timingTextColorClass(
                              getFirstResponseTimeColor(other.frt / 1000)
                            )
                          )}
                        >
                          {' '}
                          (FRT: {formatFirstResponseTime(other.frt)})
                        </span>
                      )}
                  </span>
                }
              />
            )}
        </div>

        {/* Refund details (type=6) */}
        {isRefund && other && (other.task_id || other.reason) && (
          <DetailSection label={t('Refund Details')}>
            {other.task_id && (
              <DetailRow label={t('Task ID')} value={other.task_id} mono />
            )}
            {other.reason && (
              <DetailRow
                label={t('Reason')}
                value={translateServerText(t, other.reason)}
              />
            )}
          </DetailSection>
        )}

        {/* Top-up audit info (type=1, admin only) */}
        {showTopupAuditSection && (
          <DetailSection
            icon={<ShieldCheck className='size-3.5' aria-hidden='true' />}
            iconTone='success'
            label={t('Top-up Audit Info')}
          >
            {topupAuditFields.map((field) => (
              <DetailRow
                key={field.label}
                label={field.label}
                value={field.value}
                mono
              />
            ))}
            {showLegacyTopupWarning && (
              <div className='flex items-start gap-1.5 text-xs text-amber-600 dark:text-amber-400'>
                <Info className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
                <span>
                  {t(
                    'This historical record predates audit-info tracking and cannot be backfilled. The current instance already records server IP, callback IP, payment method, and system version for new top-ups going forward.'
                  )}
                </span>
              </div>
            )}
          </DetailSection>
        )}

        {quotaOperation && (
          <DetailSection label={t('Quota adjustment details')}>
            <AuditDetailFields fields={quotaOperation.fields} />
          </DetailSection>
        )}

        {/* Manage operator (type=3, admin only) */}
        {manageOperator && (
          <DetailRow
            label={
              <span className='flex items-center gap-1.5'>
                <UserCog
                  className='text-muted-foreground size-3.5'
                  aria-hidden='true'
                />
                {t('Operator Admin')}
              </span>
            }
            value={manageOperator}
            mono
          />
        )}

        {/* Operation audit info (type=3, admin only) */}
        {showManageAuditSection && (
          <DetailSection
            icon={<ShieldCheck className='size-3.5' aria-hidden='true' />}
            iconTone='info'
            label={t('Operation Audit Info')}
          >
            {operationText != null && (
              <DetailRow label={t('Operation')} value={operationText} />
            )}
            {authMethodLabel !== '' && (
              <DetailRow
                label={t('Authentication Method')}
                value={authMethodLabel}
              />
            )}
            {changedFieldsText !== '' && (
              <DetailRow
                label={t('Changed Fields')}
                value={changedFieldsText}
              />
            )}
            {auditRoute?.method && auditRoute?.route && (
              <DetailRow
                label={t('Request')}
                value={`${auditRoute.method} ${auditRoute.route}`}
                mono
              />
            )}
            {auditRoute?.status != null && (
              <DetailRow
                label={t('Result')}
                value={
                  auditRoute.success
                    ? `${t('Success')} (${auditRoute.status})`
                    : `${t('Failed')} (${auditRoute.status})`
                }
                mono
              />
            )}
          </DetailSection>
        )}

        {/* Login audit info (type=7) */}
        {isLogin && loginAuditFields.length > 0 && (
          <DetailSection
            icon={<LogIn className='size-3.5' aria-hidden='true' />}
            iconTone='info'
            label={t('Login Info')}
          >
            {operationText != null && (
              <DetailRow label={t('Operation')} value={operationText} />
            )}
            {loginAuditFields.map((field) => (
              <DetailRow
                key={field.label}
                label={field.label}
                value={field.value}
                mono
              />
            ))}
          </DetailSection>
        )}

        {other && (
          <LogBillingEvidence
            log={props.log}
            other={other}
            isAdmin={props.isAdmin}
            legacy
            testLog={props.source === 'test'}
          />
        )}

        <LogRequestDiagnostics
          {...props}
          other={other}
          standalone
          content={details}
        />
      </div>
    </section>
  )
}

export function DetailsDialog(props: DetailsDialogProps) {
  const { t } = useTranslation()
  const typeConfig = getLogTypeConfig(props.log.type, props.source)
  const requestOverview =
    props.source !== 'test' && isTimingLogType(props.log.type)
  const other = parseLogOther(props.log.other)
  const isTieredBilling = other?.billing_mode === 'tiered_expr'
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        <>
          {t('Log Details')}
          <StatusBadge
            label={t(typeConfig.label)}
            variant={typeConfig.color as StatusBadgeProps['variant']}
            size='sm'
            copyable={false}
          />
        </>
      }
      description={t('View the complete details for this log entry')}
      contentClassName={cn(
        'min-w-0 overflow-hidden',
        'max-sm:max-h-(--dialog-available-height) max-sm:w-[calc(100vw-1.5rem)] max-sm:max-w-[calc(100vw-1.5rem)] max-sm:p-4',
        requestOverview || isTieredBilling
          ? 'sm:max-w-[min(96vw,64rem)]'
          : 'sm:max-w-lg'
      )}
      headerClassName='max-sm:gap-1'
      titleClassName='flex items-center gap-2 pe-8 text-base'
      descriptionClassName='sr-only'
      contentHeight={
        requestOverview ? 'min(78dvh, 52rem)' : 'min(72dvh, 720px)'
      }
      bodyClassName='px-1'
    >
      {props.open &&
        (requestOverview ? (
          <LogRequestDetailTabs
            key={props.log.request_id || props.log.id}
            log={props.log}
            other={other}
            isAdmin={props.isAdmin}
            initialTab={props.initialView ?? 'overview'}
          >
            <LogRequestDiagnostics {...props} other={other} />
          </LogRequestDetailTabs>
        ) : (
          <LegacyLogDetails {...props} other={other} />
        ))}
    </Dialog>
  )
}
