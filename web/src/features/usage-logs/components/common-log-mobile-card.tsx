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
import { flexRender, type Cell } from '@tanstack/react-table'
import { ChevronRight, KeyRound } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { formatAdminUserLabel } from '@/features/users/lib/admin-nickname'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { formatLogQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { formatModelName, getLogMetadata, logTokenName } from '../lib/format'
import { isDisplayableLogType, isTimingLogType } from '../lib/utils'
import { LogGroupLabel } from './log-identity-cells'
import { useLogValueFilter } from './log-value-filter'
import { ModelBadge, ResponseModelDetails } from './model-badge'
import { RequestMetadataTags } from './request-metadata-tags'
import { TokenPerformanceCell } from './token-performance-cell'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

type FieldName = 'model' | 'cost' | 'user' | 'channel' | 'token' | 'group'
type LogField = {
  label: string
  value: string
  visible: boolean
  sensitive?: boolean
}

/** Mobile summaries use tappable fields; desktop tooltip cells cannot reveal full text on touch. */
export function CommonLogMobileCard<TData>(props: {
  log: UsageLog
  cells: Map<string, Cell<TData, unknown>>
  detailsCell?: Cell<TData, unknown>
}) {
  const { t } = useTranslation()
  const context = useUsageLogsContext()
  const { isAdminView, logSource } = useLogsViewScope()
  const [selectedField, setSelectedField] = useState<FieldName | null>(null)
  const log = props.log
  const other = getLogMetadata(log)
  const displayable = log.in_progress || isDisplayableLogType(log.type)
  const timing = log.in_progress || isTimingLogType(log.type)
  const model = formatModelName(log)
  const group = log.group || other?.group || ''
  const fields: Record<FieldName, LogField> = {
    model: {
      label: t('Model'),
      value: model.name,
      visible: displayable && props.cells.has('model_name') && !!model.name,
    },
    cost: {
      label: t('Cost'),
      value: formatLogQuota(log.quota),
      visible: displayable && props.cells.has('quota'),
    },
    user: {
      label: t('User'),
      value:
        isAdminView && context.sensitiveVisible
          ? formatAdminUserLabel(log.username, log.user_admin_nickname)
          : log.username,
      visible:
        isAdminView &&
        props.cells.has(logSource === 'usage' ? 'username' : 'token_name') &&
        !!log.username,
      sensitive: true,
    },
    channel: {
      label: t('Channel'),
      value: [log.channel_name, `#${log.channel}`].filter(Boolean).join(' '),
      visible: displayable && props.cells.has('channel'),
      sensitive: true,
    },
    token: {
      label: t('Token'),
      value: logTokenName(log, t),
      visible: displayable && props.cells.has('token_name') && !!log.token_name,
      sensitive: true,
    },
    group: {
      label: t('Group'),
      value: group,
      visible:
        displayable &&
        props.cells.has(logSource === 'usage' ? 'group' : 'token_name') &&
        !!group,
      sensitive: true,
    },
  }
  const selected = selectedField ? fields[selectedField] : undefined
  const activeField =
    selected?.visible && (!selected.sensitive || context.sensitiveVisible)
      ? selected
      : undefined
  const fieldFilters = {
    user: useLogValueFilter(
      'username',
      isAdminView ? log.username : undefined,
      fields.user.value
    ),
    channel: useLogValueFilter(
      'channel',
      log.channel > 0 ? String(log.channel) : undefined,
      fields.channel.value
    ),
    token: useLogValueFilter('token', log.token_name),
    group: useLogValueFilter('group', log.group),
  }
  const metadata: (keyof typeof fieldFilters)[] = [
    'user',
    'channel',
    'token',
    'group',
  ]
  const visibleMetadata = metadata.filter((id) => fields[id].visible)
  const timeCell = props.cells.get('created_at')
  const costCell = props.cells.get('quota')
  const contentCell = props.cells.get('content')
  const channelCell = props.cells.get('channel')
  const sourceCell = props.cells.get('request_source')
  const transportCell = props.cells.get('is_stream')

  return (
    <div className='min-w-0 space-y-2.5 text-sm leading-5'>
      <div className='grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-2 has-[[data-log-event]]:[&>div]:col-span-full'>
        <div className='flex min-w-0 flex-wrap items-start gap-x-3 gap-y-2'>
          {fields.model.visible && (
            <div className='min-w-0 flex-[1_1_10rem]'>
              <ModelBadge
                modelName={model.name}
                filterValue={log.model_name}
                actualModel={model.actualModel}
                responseModel={model.responseModel}
                wrapText
                onInspect={() => setSelectedField('model')}
              />
            </div>
          )}
          {fields.cost.visible && costCell && (
            <div className='ml-auto max-w-full min-w-0 self-center'>
              {flexRender(
                costCell.column.columnDef.cell,
                costCell.getContext()
              )}
            </div>
          )}
        </div>
        {props.detailsCell && (
          <div className='[&_[data-log-event]]:mr-auto [&_[data-log-event]]:max-w-[min(100%,20rem)] [&_[data-log-event]]:items-start [&_[data-log-event]]:justify-center [&_[data-request-detail-caption]]:block [&_button]:min-h-11 [&_button]:min-w-11 [&_button]:flex-col [&_button]:gap-1'>
            {flexRender(
              props.detailsCell.column.columnDef.cell,
              props.detailsCell.getContext()
            )}
          </div>
        )}
      </div>
      {displayable && (
        <div className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 empty:hidden'>
          {(['reasoning_effort', 'invoked_tools'] as const).map(
            (field) =>
              props.cells.has(field) && (
                <RequestMetadataTags
                  key={field}
                  field={field}
                  isAdmin={isAdminView}
                  metadata={other}
                />
              )
          )}
          {sourceCell &&
            flexRender(
              sourceCell.column.columnDef.cell,
              sourceCell.getContext()
            )}
        </div>
      )}
      <div
        className='grid min-w-0 grid-cols-2 items-stretch gap-x-3'
        data-slot='log-time-and-timing'
      >
        {timeCell && (
          <div className='min-w-0'>
            {flexRender(timeCell.column.columnDef.cell, timeCell.getContext())}
          </div>
        )}
        {timing && transportCell && (
          <div className='flex items-center justify-end'>
            {flexRender(
              transportCell.column.columnDef.cell,
              transportCell.getContext()
            )}
          </div>
        )}
      </div>
      {visibleMetadata.length > 0 && (
        <div className='grid min-w-0 grid-cols-2 gap-x-4 gap-y-0.5'>
          {visibleMetadata.map((id) => {
            const field = fields[id]
            const filter = fieldFilters[id]
            let fieldContent = (
              <span
                className='truncate'
                title={
                  id === 'user' && context.sensitiveVisible
                    ? log.username
                    : undefined
                }
              >
                {id === 'user' && isAdminView && context.sensitiveVisible
                  ? log.user_admin_nickname?.trim() || log.username
                  : field.value}
              </span>
            )
            if (id === 'group') {
              fieldContent = (
                <LogGroupLabel
                  group={field.value}
                  other={other}
                  visible={context.sensitiveVisible}
                />
              )
            } else if (id === 'token') {
              fieldContent = (
                <StatusBadge
                  label={field.value}
                  copyable={false}
                  icon={KeyRound}
                  className='border-border/60 bg-muted/30 text-foreground max-w-full rounded-md border px-1.5 py-0.5 text-sm'
                />
              )
            }
            return (
              <div key={id} className='flex min-w-0 items-center gap-2'>
                <span className='text-muted-foreground max-w-[40%] shrink-0 text-xs [overflow-wrap:anywhere]'>
                  {id === 'user' ? (
                    <Avatar className='ring-border/60 size-6 shrink-0 ring-1'>
                      <AvatarFallback
                        className='text-[11px] font-semibold'
                        style={
                          context.sensitiveVisible
                            ? getUserAvatarStyle(log.username)
                            : undefined
                        }
                      >
                        {context.sensitiveVisible
                          ? getUserAvatarFallback(log.username)
                          : '•'}
                      </AvatarFallback>
                    </Avatar>
                  ) : (
                    field.label
                  )}
                </span>
                {context.sensitiveVisible ? (
                  <>
                    <Button
                      variant='ghost'
                      aria-label={
                        filter?.['aria-label'] ??
                        `${field.label}: ${field.value}`
                      }
                      aria-pressed={filter?.['aria-pressed']}
                      aria-haspopup={filter ? undefined : 'dialog'}
                      title={filter?.title}
                      onClick={filter?.onClick ?? (() => setSelectedField(id))}
                      className={cn(
                        'text-foreground h-auto min-h-8 min-w-0 flex-1 shrink justify-start px-0 py-1 text-left text-sm font-normal',
                        filter?.active && 'bg-primary/10 ring-primary/25 ring-1'
                      )}
                    >
                      {fieldContent}
                    </Button>
                    {filter && (
                      <Button
                        variant='ghost'
                        size='icon'
                        className='size-8 shrink-0'
                        aria-label={`${t('Details')}: ${field.label}`}
                        aria-haspopup='dialog'
                        onClick={() => setSelectedField(id)}
                      >
                        <ChevronRight aria-hidden className='size-3.5' />
                      </Button>
                    )}
                  </>
                ) : (
                  <span className='min-w-0 py-1.5'>••••</span>
                )}
              </div>
            )
          })}
        </div>
      )}
      {props.cells.has('prompt_tokens') && (
        <TokenPerformanceCell
          log={log}
          other={other}
          isAdmin={isAdminView}
          compact
        />
      )}
      {contentCell && (
        <div className='relative min-w-0 border-t pt-2 [&_button]:min-h-8 [&_button]:w-full [&_button]:max-w-full [&_button]:pr-5 [&_button]:text-sm [&_button>span]:line-clamp-2 [&_button>span]:[overflow-wrap:anywhere] [&_button>span]:whitespace-normal'>
          {flexRender(
            contentCell.column.columnDef.cell,
            contentCell.getContext()
          )}
          <ChevronRight
            aria-hidden='true'
            className='text-primary pointer-events-none absolute top-4 right-0 size-4'
          />
        </div>
      )}
      <Dialog
        open={!!activeField}
        onOpenChange={(open) => {
          if (!open) setSelectedField(null)
        }}
        title={activeField?.label ?? t('Details')}
        contentClassName='max-sm:top-auto max-sm:bottom-0 max-sm:max-h-[85dvh] max-sm:max-w-full max-sm:translate-y-0 max-sm:rounded-b-none max-sm:rounded-t-2xl max-sm:pb-[max(1rem,env(safe-area-inset-bottom))] [&_[data-slot=dialog-close]]:size-11'
        footer={
          activeField && (
            <CopyButton
              value={activeField.value}
              variant='default'
              size='default'
              className='min-h-11 w-full'
            >
              {t('Copy')}
            </CopyButton>
          )
        }
      >
        {activeField && (
          <div className='space-y-4'>
            <p className='bg-muted rounded-lg p-4 text-base [overflow-wrap:anywhere] whitespace-pre-wrap'>
              {activeField.value}
            </p>
            {selectedField === 'model' && model.responseModel && (
              <ResponseModelDetails observation={model.responseModel} />
            )}
            {selectedField === 'model' &&
              !model.responseModel &&
              model.actualModel && (
                <div className='space-y-2'>
                  <p className='text-muted-foreground'>{t('Actual Model')}</p>
                  <p className='text-base [overflow-wrap:anywhere]'>
                    {model.actualModel}
                  </p>
                  <CopyButton value={model.actualModel} />
                </div>
              )}
            {selectedField === 'channel' && channelCell && (
              <div>
                {flexRender(
                  channelCell.column.columnDef.cell,
                  channelCell.getContext()
                )}
              </div>
            )}
            {selectedField === 'user' && (
              <Button
                variant='outline'
                className='min-h-11'
                onClick={() => {
                  setSelectedField(null)
                  context.setSelectedUserId(log.user_id)
                  context.setUserInfoDialogOpen(true)
                }}
              >
                {t('User Information')}
              </Button>
            )}
          </div>
        )}
      </Dialog>
    </div>
  )
}
