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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { toIntlLocale } from '@/i18n/languages'
import {
  formatNumber,
  formatTimestampToDate,
  formatUseTime,
} from '@/lib/format'
import { cn } from '@/lib/utils'

import { LOG_TYPE_ENUM } from '../constants'
import type { UsageLog } from '../data/schema'
import { getLogTokenTotals } from '../lib/cache-usage'
import { formatFirstResponseTime, logTokenName } from '../lib/format'
import { getLogTypeConfig, isTimingLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { LogOverviewCard } from './dialogs/log-detail-layout'
import { LogGroupLabel } from './log-identity-cells'
import { RequestMetadataTags } from './request-metadata-tags'
import { StreamModeCell } from './timing-metrics-cell'

interface InfoField {
  label: string
  value?: ReactNode
  wide?: boolean
  copyable?: boolean
  mono?: boolean
}

function RequestInfoCard(props: {
  title: string
  fields: InfoField[]
  wide?: boolean
}) {
  const { t } = useTranslation()
  const fields = props.fields.filter(
    ({ value }) =>
      value !== undefined && value !== null && value !== false && value !== ''
  )
  if (!fields.length) return null
  return (
    <LogOverviewCard
      title={props.title}
      className={props.wide ? 'md:col-span-2' : undefined}
    >
      <dl className='grid min-w-0 grid-cols-2 items-start gap-x-4 gap-y-3 max-[350px]:grid-cols-1'>
        {fields.map((field) => (
          <div
            key={field.label}
            className={cn(
              'min-w-0 space-y-0.5',
              field.wide &&
                (props.wide ? 'col-span-full md:col-span-1' : 'col-span-full')
            )}
          >
            <dt className='text-muted-foreground text-xs leading-4'>
              {field.label}
            </dt>
            <dd className='flex min-w-0 items-start gap-1 text-[13px] leading-5'>
              <div
                className={cn(
                  'min-w-0 font-medium [overflow-wrap:anywhere]',
                  field.mono && 'font-mono text-xs leading-5 tabular-nums'
                )}
              >
                {field.value}
              </div>
              {field.copyable && (
                <CopyButton
                  value={String(field.value)}
                  aria-label={`${t('Copy to clipboard')} · ${field.label}`}
                  className='text-muted-foreground size-5'
                  iconClassName='size-3'
                />
              )}
            </dd>
          </div>
        ))}
      </dl>
    </LogOverviewCard>
  )
}

export function LogRequestInfoCards(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
  showFailure?: boolean
}) {
  const { t, i18n } = useTranslation()
  const { log, other, isAdmin } = props
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const admin = isAdmin ? other?.admin_info : undefined
  const timing = isTimingLogType(log.type)
  const observed = other?.stream_status
  const group = log.group || other?.group
  const channel =
    isAdmin && log.channel > 0
      ? [`#${log.channel}`, log.channel_name].filter(Boolean).join(' · ')
      : undefined
  const lastDecision = Array.isArray(admin?.request_policy)
    ? admin.request_policy.at(-1)
    : undefined
  const recordedStatus =
    log.type === LOG_TYPE_ENUM.ERROR
      ? /^status_code=([1-5]\d{2})(?:,|$)/i.exec(log.content)?.[1]
      : undefined
  const exactDuration = other?.duration_ms
  let duration: string | undefined
  if (
    timing &&
    typeof exactDuration === 'number' &&
    Number.isFinite(exactDuration) &&
    exactDuration >= 0
  ) {
    duration =
      exactDuration < 1000
        ? `${formatNumber(exactDuration, locale)}ms`
        : `${formatNumber(exactDuration / 1000, locale)}s`
  } else if (timing && log.use_time > 0) {
    duration = `≈ ${formatUseTime(log.use_time)}`
  }
  const first = other?.frt
  const total = timing ? getLogTokenTotals(log, other).total : null
  const observation = isAdmin ? other?.response_model : undefined
  const requestedModel = observation?.requested_model || log.model_name
  const hasNamedTools =
    Array.isArray(other?.invoked_tools) &&
    other.invoked_tools.some((name) => typeof name === 'string' && name.trim())
  const hasToolObservation =
    hasNamedTools ||
    other?.tool_observation === 'partial' ||
    (other?.tool_observation === 'complete' &&
      Array.isArray(other.invoked_tools) &&
      other.invoked_tools.length === 0)
  return (
    <>
      <RequestInfoCard
        title={t('Request information')}
        fields={[
          { label: t('Account'), value: isAdmin && log.username },
          {
            label: t('Token Name'),
            value: log.token_name && logTokenName(log, t),
          },
          { label: t('Status Code'), value: recordedStatus, mono: true },
          { label: t('Log Type'), value: t(getLogTypeConfig(log.type).label) },
          {
            label: t('Reject Reason'),
            value: props.showFailure !== false && admin?.reject_reason && (
              <span className='text-destructive whitespace-pre-wrap'>
                {admin.reject_reason}
              </span>
            ),
            wide: true,
          },
          {
            label: t('Time'),
            value:
              log.created_at > 0
                ? formatTimestampToDate(log.created_at)
                : undefined,
            copyable: true,
            mono: true,
          },
          {
            label: t('Endpoint'),
            value: isAdmin && other?.request_path,
            mono: true,
          },
        ]}
      />
      <RequestInfoCard
        title={t('Transport and usage')}
        fields={[
          {
            label: t('Response mode'),
            value: timing && (
              <StreamModeCell
                logType={log.type}
                isStream={log.is_stream}
                metadata={other}
                showEndpoint={false}
              />
            ),
          },
          {
            label: t('Total Tokens'),
            value: total != null ? formatNumber(total, locale) : undefined,
            mono: true,
          },
          { label: t('Duration'), value: duration, mono: true },
          {
            label: t('First token'),
            value:
              timing &&
              log.is_stream &&
              typeof first === 'number' &&
              Number.isFinite(first) &&
              (first > 0 || (first === 0 && exactDuration != null)) &&
              formatFirstResponseTime(first),
            mono: true,
          },
          {
            label: t('Stream Status'),
            value: observed?.response_status || observed?.status,
          },
        ]}
      />
      <RequestInfoCard
        title={t('Model and routing')}
        fields={[
          { label: t('Request Model'), value: requestedModel, mono: true },
          {
            label: t('Reasoning Effort'),
            value: other?.reasoning_effort,
            mono: true,
          },
          {
            label: t('Upstream Model'),
            value:
              observation?.upstream_model ||
              (isAdmin && other?.upstream_model_name),
            mono: true,
          },
          {
            label: t('Response Model'),
            value: observation?.returned_model,
            mono: true,
          },
          { label: t('Channel'), value: channel },
          {
            label: t('Group'),
            value: group && (
              <LogGroupLabel group={group} other={other} visible />
            ),
          },
          {
            label: t('Retry Chain'),
            value:
              Array.isArray(admin?.use_channel) &&
              admin.use_channel.length > 0 &&
              admin.use_channel.join(' → '),
            wide: true,
            mono: true,
          },
        ]}
      />
      <RequestInfoCard
        title={t('Client and upstream')}
        fields={[
          { label: t('IP Address'), value: timing && log.ip, mono: true },
          { label: t('Client'), value: isAdmin && other?.client_tool },
          {
            label: t('Attempt'),
            value:
              lastDecision && lastDecision.attempt > 0
                ? formatNumber(lastDecision.attempt, locale)
                : undefined,
            mono: true,
          },
          {
            label: t('Tools'),
            value: isAdmin && hasToolObservation && (
              <div className='w-fit'>
                <RequestMetadataTags
                  isAdmin
                  expanded={!hasNamedTools}
                  field='invoked_tools'
                  metadata={other}
                />
              </div>
            ),
          },
          {
            label: t('User Agent'),
            value: isAdmin && (admin?.user_agent || other?.user_agent),
            wide: true,
            mono: true,
          },
        ]}
      />
    </>
  )
}

export function LogRequestIdentifiers(props: {
  log: UsageLog
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const { log, isAdmin } = props
  return (
    <RequestInfoCard
      title={t('Request identifiers')}
      wide
      fields={[
        {
          label: t('Request ID'),
          value: log.request_id,
          wide: true,
          copyable: true,
          mono: true,
        },
        {
          label: t('Upstream Request ID'),
          value: isAdmin && log.upstream_request_id,
          wide: true,
          copyable: true,
          mono: true,
        },
        {
          label: t('User ID'),
          value: isAdmin && log.user_id > 0 && log.user_id,
          copyable: true,
          mono: true,
        },
        {
          label: t('Token ID'),
          value: isAdmin && log.token_id > 0 && log.token_id,
          copyable: true,
          mono: true,
        },
      ]}
    />
  )
}
