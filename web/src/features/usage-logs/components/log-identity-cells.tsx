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

import { TruncatedCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { getLogMetadata, logTokenName } from '../lib/format'
import { isDisplayableLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { useUsageLogsContext } from './usage-logs-provider'

export function LogGroupLabel({
  group,
  other,
  visible,
}: {
  group: string
  other: LogOtherData | null
  visible: boolean
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
        <GroupBadge
          group={visible ? group : '••••'}
          type='text'
          size='sm'
          className='text-muted-foreground dark:text-muted-foreground max-w-36 truncate text-xs font-normal'
        />
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
}: {
  log: UsageLog
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()
  const other = getLogMetadata(log)
  const displayable = isDisplayableLogType(log.type)
  const token = displayable && logTokenName(log, t)
  return (
    <div className='flex max-w-full min-w-0 flex-col items-start justify-center gap-[3px] text-xs leading-snug'>
      {isAdmin && log.username && (
        <button
          type='button'
          className='hover:text-primary inline-flex max-w-full items-center gap-1.5 font-semibold'
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
          <span className='truncate'>
            {sensitiveVisible ? log.username : '••••'}
          </span>
        </button>
      )}
      {token && (
        <div className='text-muted-foreground inline-flex max-w-full items-center gap-1.5'>
          <KeyRound aria-hidden className='size-3.5 shrink-0' />
          <StatusBadge
            type='text'
            size='sm'
            showDot={false}
            variant='neutral'
            className='text-muted-foreground dark:text-muted-foreground font-mono text-[11px]'
            copyable={sensitiveVisible && !!log.token_name}
            copyText={sensitiveVisible ? log.token_name : undefined}
            label={sensitiveVisible ? token : '••••'}
          />
        </div>
      )}
      {displayable && (
        <LogGroupLabel
          group={log.group || other?.group || ''}
          other={other}
          visible={sensitiveVisible}
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
  const hasClient = isAdmin && isDisplayableLogType(log.type)
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
        <div className='flex min-w-0 items-center gap-1.5 font-semibold text-slate-700 dark:text-slate-300'>
          <ClientIcon aria-label={t('Client')} className='size-3.5 shrink-0' />
          <TruncatedCell tabIndex={0}>
            {sensitiveVisible ? client || t('Unidentified source') : '••••'}
          </TruncatedCell>
        </div>
      )}
      {details.map(
        ({ id, short, label, value, allowed }) =>
          allowed &&
          visibility[id] !== false && (
            <div
              key={id}
              className='grid min-w-0 grid-cols-[2ch_minmax(0,1fr)] items-baseline gap-x-1.5 font-mono text-[11px]'
            >
              <span aria-label={label} className='text-muted-foreground'>
                {short}
              </span>
              {typeof value === 'string' && value.trim() ? (
                <TruncatedCell
                  tabIndex={0}
                  cellClassName={cn(
                    id === 'user_agent' && 'text-muted-foreground'
                  )}
                >
                  {sensitiveVisible ? value : '••••'}
                </TruncatedCell>
              ) : (
                <span
                  className='text-muted-foreground/70'
                  title={t('Not recorded')}
                >
                  —
                </span>
              )}
            </div>
          )
      )}
    </div>
  )
}
