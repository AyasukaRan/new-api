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
import type { VisibilityState } from '@tanstack/react-table'
import {
  Braces,
  Link,
  Monitor,
  Network,
  KeyRound,
  Tags,
  Terminal,
  UserRound,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { TruncatedCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { formatAdminUserLabel } from '@/features/users/lib/admin-nickname'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { getLogMetadata, logTokenName } from '../lib/format'
import { isDisplayableLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { LogFilterValue, useLogValueFilter } from './log-value-filter'
import { RequestPurposeTag } from './request-metadata-tags'
import { useUsageLogsContext } from './usage-logs-provider'

export function LogGroupLabel({
  group,
  other,
  visible,
  filterValue,
}: {
  group: string
  other: LogOtherData | null
  visible: boolean
  filterValue?: string
}) {
  const { t, i18n } = useTranslation()
  const ratio = [other?.user_group_ratio, other?.group_ratio].find(
    (r): r is number => typeof r === 'number' && Number.isFinite(r) && r >= 0
  )
  if (!group && ratio == null) return null
  return (
    <div className='text-muted-foreground inline-flex max-w-full min-w-0 items-center gap-1.5 text-xs'>
      <Tags aria-hidden className='size-3.5 shrink-0' />
      {group && (
        <LogFilterValue
          field='group'
          value={visible ? filterValue : undefined}
          label={group}
          className='shrink'
        >
          <GroupBadge
            group={visible ? group : '••••'}
            type='text'
            size='sm'
            className='text-muted-foreground dark:text-muted-foreground max-w-36 truncate text-xs font-normal'
          />
        </LogFilterValue>
      )}
      {ratio != null && (
        <span
          title={t('Group Ratio')}
          className={cn(
            'shrink-0 rounded px-1 font-mono font-semibold tabular-nums',
            ratio < 1 &&
              'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
            ratio > 1 && 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
            ratio === 1 && 'bg-muted text-muted-foreground'
          )}
        >
          {ratio.toLocaleString(
            toIntlLocale(i18n.resolvedLanguage || i18n.language),
            { maximumFractionDigits: 4 }
          )}
          ×
        </span>
      )}
    </div>
  )
}

export function LogAccountCell({
  log,
  isAdmin,
  field,
}: {
  log: UsageLog
  isAdmin: boolean
  field?: 'user' | 'token' | 'group'
}) {
  const { t } = useTranslation()
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()
  const other = getLogMetadata(log)
  const displayable = log.in_progress || isDisplayableLogType(log.type)
  const token = displayable && logTokenName(log, t)
  const account =
    isAdmin && sensitiveVisible
      ? formatAdminUserLabel(log.username, log.user_admin_nickname)
      : undefined
  const displayName =
    isAdmin && sensitiveVisible
      ? log.user_admin_nickname?.trim() || log.username
      : undefined
  const usernameFilter = useLogValueFilter(
    'username',
    isAdmin && sensitiveVisible ? log.username : undefined,
    account
  )
  const tokenFilter = useLogValueFilter(
    'token',
    sensitiveVisible && displayable ? log.token_name : undefined
  )
  return (
    <div
      className={cn(
        'flex max-w-full min-w-0 flex-col items-start justify-center gap-[3px] text-xs leading-snug',
        field && 'w-max',
        (field === 'user' || field === 'token') && 'max-w-28',
        field === 'group' && 'max-w-40'
      )}
    >
      {(!field || field === 'user') && isAdmin && log.username && (
        <div className='inline-flex max-w-full items-center gap-1.5 text-[13px] font-semibold'>
          <Button
            variant='ghost'
            aria-label={usernameFilter ? t('User Information') : undefined}
            className='hover:text-primary h-auto min-w-0 gap-1.5 p-0 text-[13px] font-semibold'
            onClick={(e) => {
              e.stopPropagation()
              setSelectedUserId(log.user_id)
              setUserInfoDialogOpen(true)
            }}
          >
            <UserRound
              className='text-muted-foreground size-3.5 shrink-0'
              aria-hidden
            />
            {!usernameFilter && (
              <span
                className='truncate'
                title={sensitiveVisible ? log.username : undefined}
              >
                {displayName ?? '••••'}
              </span>
            )}
          </Button>
          {usernameFilter && (
            <LogFilterValue
              field='username'
              value={log.username}
              label={account}
              className='shrink'
            >
              <span className='truncate' title={log.username}>
                {displayName}
              </span>
            </LogFilterValue>
          )}
        </div>
      )}
      {(!field || field === 'token') && token && (
        <div className='group/log-token text-muted-foreground inline-flex max-w-full items-center gap-1.5'>
          <KeyRound aria-hidden className='size-3.5 shrink-0' />
          <LogFilterValue
            field='token'
            value={sensitiveVisible ? log.token_name : undefined}
            className='shrink'
          >
            <StatusBadge
              type='text'
              size='sm'
              showDot={false}
              variant='neutral'
              className='text-muted-foreground dark:text-muted-foreground text-xs font-normal'
              copyable={!tokenFilter && sensitiveVisible && !!log.token_name}
              copyText={sensitiveVisible ? log.token_name : undefined}
              label={sensitiveVisible ? token : '••••'}
            />
          </LogFilterValue>
          {tokenFilter && (
            <CopyButton
              value={log.token_name}
              aria-label={t('Copy {{field}}', { field: t('Token') })}
              className='size-4 opacity-0 group-hover/log-token:opacity-100 focus-visible:opacity-100'
              iconClassName='size-3'
            />
          )}
        </div>
      )}
      {(!field || field === 'group') && displayable && (
        <LogGroupLabel
          group={log.group || other?.group || ''}
          other={other}
          visible={sensitiveVisible}
          filterValue={log.group}
        />
      )}
    </div>
  )
}

const clientIcons = new Map([
  ['Claude Code', Braces],
  ['Codex Desktop', Monitor],
  ['Sub2API', Network],
  ['Go HTTP Client', Link],
])

export function LogSourceCell({
  log,
  isAdmin,
  visibility,
}: {
  log: UsageLog
  isAdmin: boolean
  visibility: VisibilityState
}) {
  const { t } = useTranslation()
  const { sensitiveVisible } = useUsageLogsContext()
  const other = getLogMetadata(log)
  const client =
    typeof other?.client_tool === 'string' ? other.client_tool.trim() : ''
  const ClientIcon = (sensitiveVisible && clientIcons.get(client)) || Terminal
  const hasClient =
    isAdmin && (log.in_progress || isDisplayableLogType(log.type))
  const clientFilter = useLogValueFilter(
    'clientTool',
    sensitiveVisible ? client : undefined
  )
  const ipFilter = useLogValueFilter(
    'ip',
    sensitiveVisible ? log.ip : undefined
  )
  const details = [
    {
      id: 'user_agent',
      short: 'UA',
      label: t('User Agent'),
      value: other?.admin_info?.user_agent || other?.user_agent,
      allowed: hasClient,
    },
    {
      id: 'ip',
      short: 'IP',
      label: t('IP Address'),
      value: log.ip,
      allowed: true,
    },
  ]
  return (
    <div className='flex max-w-full min-w-0 flex-col justify-center gap-0.5 text-xs leading-snug'>
      {hasClient && visibility.client_tool !== false && (
        <div className='flex min-w-0 items-center gap-1.5 text-[13px] font-medium'>
          <ClientIcon aria-label={t('Client')} className='size-4 shrink-0' />
          <LogFilterValue
            field='clientTool'
            value={sensitiveVisible ? client : undefined}
          >
            <TruncatedCell tabIndex={clientFilter ? undefined : 0}>
              {sensitiveVisible ? client || t('Unidentified source') : '••••'}
            </TruncatedCell>
          </LogFilterValue>
          <RequestPurposeTag
            isAdmin={isAdmin && sensitiveVisible}
            metadata={other}
          />
        </div>
      )}
      {details.map(
        ({ id, short, label, value, allowed }) =>
          allowed &&
          visibility[id] !== false && (
            <div
              key={id}
              className='grid min-w-0 grid-cols-[1rem_minmax(0,1fr)] items-baseline gap-x-1.5 font-mono text-xs font-normal'
            >
              <span
                aria-label={label}
                className='text-muted-foreground font-sans text-[10px] font-medium'
              >
                {short}
              </span>
              {typeof value === 'string' && value.trim() ? (
                <LogFilterValue
                  field='ip'
                  value={sensitiveVisible && id === 'ip' ? value : undefined}
                >
                  <TruncatedCell
                    tabIndex={id === 'ip' && ipFilter ? undefined : 0}
                    cellClassName={cn(
                      id === 'user_agent' && 'text-muted-foreground'
                    )}
                  >
                    {sensitiveVisible ? value : '••••'}
                  </TruncatedCell>
                </LogFilterValue>
              ) : (
                <span
                  className='text-muted-foreground/70'
                  title={t('Not recorded')}
                >
                  {id === 'user_agent' ? t('Not recorded') : '—'}
                </span>
              )}
            </div>
          )
      )}
    </div>
  )
}
