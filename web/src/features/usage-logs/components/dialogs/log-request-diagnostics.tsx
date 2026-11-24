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
import { Route } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { StatusBadge } from '@/components/status-badge'
import { PolicyDecisionRecord } from '@/features/system-settings/request-policies/decision-record'

import type { UsageLog } from '../../data/schema'
import {
  getParamOverrideActionLabel,
  getRetryDecisionReason,
  isFailedStreamResponse,
  parseAuditLine,
  renderLogContent,
} from '../../lib/format'
import { isResponseModelMismatch } from '../../lib/response-model'
import type { LogOtherData } from '../../types'
import { ResponseModelDetails } from '../model-badge'
import { PluginAuthorLink } from '../plugin-author-link'
import { RequestFailureSummary } from '../request-failure-summary'
import { CapturedRequestBody } from './captured-request-body'
import { DetailRow, LogOverviewCard } from './log-detail-layout'

export function LogRequestDiagnostics(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
  isRoot: boolean
  standalone?: boolean
  content?: string
}) {
  const { t } = useTranslation()
  const { log, other } = props
  const admin = props.isAdmin ? other?.admin_info : undefined
  const root = props.isRoot && props.isAdmin ? other?.root_info : undefined
  const details = props.content ?? renderLogContent(other, t) ?? log.content
  const panels: ReactNode[] = []

  if (
    log.type === 5 ||
    isFailedStreamResponse(other?.stream_status) ||
    getRetryDecisionReason(other?.retry_decision, t)
  ) {
    panels.push(
      <div key='failure' className='basis-full'>
        <RequestFailureSummary
          metadata={other}
          error={log.type === 5 ? details || '' : undefined}
        />
      </div>
    )
  }
  if (admin?.reject_reason) {
    panels.push(
      <LogOverviewCard
        key='reject'
        title={t('Reject Reason')}
        className='basis-full'
      >
        <p className='text-destructive text-sm wrap-anywhere whitespace-pre-wrap'>
          {admin.reject_reason}
        </p>
      </LogOverviewCard>
    )
  }
  if (admin?.request_policy?.length) {
    panels.push(
      <LogOverviewCard
        key='policy'
        title={t('Request policy decisions')}
        className='basis-full'
      >
        <PolicyDecisionRecord events={admin.request_policy} />
      </LogOverviewCard>
    )
  }
  const conversion = Array.isArray(other?.request_conversion)
    ? other.request_conversion.filter(Boolean)
    : []
  if (
    props.isAdmin &&
    log.type !== 6 &&
    (conversion.length || (props.standalone && other?.request_path))
  ) {
    panels.push(
      <LogOverviewCard key='conversion' title={t('Request Conversion')}>
        <div className='space-y-2'>
          {props.standalone && other?.request_path && (
            <DetailRow label={t('Path')} value={other.request_path} mono />
          )}
          {conversion.length > 0 && (
            <div className='flex min-w-0 items-center gap-2 text-xs'>
              <Route
                aria-hidden='true'
                className='text-muted-foreground size-4 shrink-0'
              />
              <span className='min-w-0 font-mono wrap-anywhere'>
                {conversion.join(' → ')}
              </span>
              {conversion.length === 1 && (
                <span className='text-muted-foreground'>
                  {t('Native format')}
                </span>
              )}
              <CopyButton
                value={conversion.join(' → ')}
                className='ms-auto shrink-0'
              />
            </div>
          )}
        </div>
      </LogOverviewCard>
    )
  }
  if (
    other?.response_model &&
    (props.standalone ||
      !props.isAdmin ||
      isResponseModelMismatch(other.response_model))
  ) {
    panels.push(
      <LogOverviewCard key='model' title={t('Response Model')}>
        <ResponseModelDetails observation={other.response_model} />
      </LogOverviewCard>
    )
  } else if (
    (props.standalone || !props.isAdmin) &&
    !other?.response_model &&
    other?.is_model_mapped &&
    other.upstream_model_name
  ) {
    panels.push(
      <LogOverviewCard key='mapping' title={t('Model Mapping')}>
        <DetailRow label={t('Request Model')} value={log.model_name} mono />
        <DetailRow
          label={t('Actual Model')}
          value={other.upstream_model_name}
          mono
        />
      </LogOverviewCard>
    )
  }
  if (other?.stream_status && other.stream_status.status !== 'ok') {
    panels.push(
      <LogOverviewCard key='stream' title={t('Stream Status')}>
        <div className='space-y-2'>
          <DetailRow
            label={t('Status')}
            value={
              <StatusBadge
                label={other.stream_status.status || t('Error')}
                variant='red'
                size='sm'
                copyable={false}
              />
            }
          />
          {other.stream_status.end_reason && (
            <DetailRow
              label={t('End Reason')}
              value={other.stream_status.end_reason}
            />
          )}
          {(other.stream_status.error_count ?? 0) > 0 && (
            <DetailRow
              label={t('Soft Errors')}
              value={String(other.stream_status.error_count)}
            />
          )}
          {Array.isArray(other.stream_status.errors) &&
            other.stream_status.errors.length > 0 && (
              <pre className='bg-background/60 mt-1 max-h-32 overflow-y-auto rounded border p-2 font-mono text-[11px] leading-relaxed wrap-break-word whitespace-pre-wrap'>
                {other.stream_status.errors.join('\n')}
              </pre>
            )}
        </div>
      </LogOverviewCard>
    )
  }
  if (admin?.task_plugin) {
    panels.push(
      <LogOverviewCard key='plugin' title={t('Task Plugin')}>
        <div className='space-y-2'>
          <DetailRow
            label={t('Plugin key')}
            value={admin.task_plugin.key}
            mono
          />
          <DetailRow label={t('Name')} value={admin.task_plugin.name} />
          {admin.task_plugin.version ? (
            <DetailRow
              label={t('Version')}
              value={admin.task_plugin.version}
              mono
            />
          ) : null}
          {admin.task_plugin.author ? (
            <DetailRow
              label={t('Plugin author')}
              value={
                <PluginAuthorLink author={admin.task_plugin.author} showUrl />
              }
            />
          ) : null}
        </div>
      </LogOverviewCard>
    )
  }
  if (root && (root.task_plugin || root.upstream_task_id || root.node_name)) {
    panels.push(
      <LogOverviewCard key='root' title={t('Root Diagnostics')}>
        <div className='space-y-2'>
          {root.task_plugin ? (
            <>
              <DetailRow
                label={t('API Version')}
                value={String(root.task_plugin.api_version)}
                mono
              />
              <DetailRow
                label={t('Plugin Generation')}
                value={String(root.task_plugin.generation)}
                mono
              />
            </>
          ) : null}
          {root.upstream_task_id ? (
            <DetailRow
              label={t('Upstream Task ID')}
              value={root.upstream_task_id}
              mono
            />
          ) : null}
          {root.node_name ? (
            <DetailRow label={t('Node Name')} value={root.node_name} mono />
          ) : null}
        </div>
      </LogOverviewCard>
    )
  }
  if (other?.is_system_prompt_overwritten) {
    panels.push(
      <LogOverviewCard key='prompt' title={t('System Prompt')}>
        <StatusBadge
          label={t('Overwritten')}
          variant='orange'
          size='sm'
          copyable={false}
        />
      </LogOverviewCard>
    )
  }
  const overrides = []
  const occurrences = new Map<string, number>()
  for (const line of Array.isArray(other?.po) ? other.po : []) {
    const entry = line && parseAuditLine(line)
    if (!entry) continue
    const occurrence = occurrences.get(line) ?? 0
    occurrences.set(line, occurrence + 1)
    overrides.push({ ...entry, key: `${occurrence}:${line}` })
  }
  if (overrides.length) {
    panels.push(
      <LogOverviewCard
        key='overrides'
        title={`${t('Param Override')} (${overrides.length})`}
        className='basis-full'
      >
        <ul className='divide-border divide-y'>
          {overrides.map((entry) => (
            <li
              key={entry.key}
              className='flex min-w-0 flex-wrap items-baseline gap-2 py-2 first:pt-0 last:pb-0'
            >
              <StatusBadge
                variant='neutral'
                label={getParamOverrideActionLabel(entry.action, t)}
                copyable={false}
              />
              <span className='font-mono text-xs wrap-anywhere'>
                {entry.content}
              </span>
            </li>
          ))}
        </ul>
      </LogOverviewCard>
    )
  }
  if (details && log.type !== 5) {
    panels.push(
      <LogOverviewCard
        key='content'
        title={t('Content')}
        className='basis-full'
      >
        <div className='flex items-start gap-2'>
          <p className='min-w-0 flex-1 text-sm leading-relaxed wrap-anywhere whitespace-pre-wrap'>
            {details}
          </p>
          <CopyButton value={details} className='shrink-0' />
        </div>
      </LogOverviewCard>
    )
  }
  if (
    admin &&
    !admin.trace_id?.trim() &&
    (admin.request_body || admin.request_body_ref)
  ) {
    panels.push(
      <div
        key={JSON.stringify([
          log.id,
          log.created_at,
          log.request_id,
          admin.request_body_ref,
        ])}
        className='basis-full'
      >
        <CapturedRequestBody log={log} adminInfo={admin} />
      </div>
    )
  }
  if (!panels.length) {
    return props.standalone ? null : (
      <EmptyState
        icon={Route}
        className='min-h-36'
        title={t('No diagnostics recorded for this request')}
      />
    )
  }
  return (
    <div className='flex min-w-0 flex-wrap items-start gap-3 *:w-full *:min-w-0 *:grow md:*:w-[calc(50%_-_0.375rem)]'>
      {panels}
    </div>
  )
}
