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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, Trash2, Power, PowerOff } from 'lucide-react'
import { useState, useEffect, useId, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getMultiKeyStatus,
  enableMultiKey,
  disableMultiKey,
  deleteMultiKey,
  enableAllMultiKeys,
  disableAllMultiKeys,
  deleteDisabledMultiKeys,
  updateChannelBalance,
  setMultiKeyGroups,
  setMultiKeyCostRatio,
} from '../../api'
import { MULTI_KEY_FILTER_OPTIONS } from '../../constants'
import {
  channelsQueryKeys,
  refreshChannelStatusQueries,
  formatTimestamp,
  getMultiKeyStatusConfig,
  getMultiKeyConfirmMessage,
  isDestructiveAction,
} from '../../lib'
import type {
  Channel,
  KeyStatus,
  MultiKeyConfirmAction,
  MultiKeyStatusResponse,
} from '../../types'
import { useChannels } from '../channels-provider'
import { StatisticsCard } from './multi-key-statistics-card'
import { MultiKeyTableRowActions } from './multi-key-table-row-actions'

type MultiKeyManageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function MultiKeyManageDialog({
  open,
  onOpenChange,
}: MultiKeyManageDialogProps) {
  const { currentRow } = useChannels()
  const userId = useAuthStore((s) => s.auth.user?.id)
  if (!open || !currentRow) return null
  return (
    <MultiKeyManageContent
      key={`${userId}:${currentRow.id}`}
      channel={currentRow}
      onOpenChange={onOpenChange}
    />
  )
}

function MultiKeyManageContent({
  channel,
  onOpenChange,
}: {
  channel: Channel
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const { sensitiveVisible } = useChannels()
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.auth.user)
  const canEditSensitive = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [currentPage, setCurrentPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<number | null>(null)
  const [confirmation, setConfirmation] = useState<
    | (MultiKeyConfirmAction & {
        statusSnapshot: MultiKeyStatusResponse['data']
      })
    | null
  >(null)
  const [isPerformingAction, setIsPerformingAction] = useState(false)
  const [isUpdatingBalance, setIsUpdatingBalance] = useState(false)
  const [groupDrafts, setGroupDrafts] = useState<
    Record<string, string[] | undefined>
  >({})
  const [costDrafts, setCostDrafts] = useState<
    Record<string, string | undefined>
  >({})
  const instanceId = useId()
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const statusQuery = useQuery({
    queryKey: [
      ...channelsQueryKeys.detail(channel.id),
      'multi-keys',
      instanceId,
      currentPage,
      statusFilter,
    ],
    queryFn: async ({ signal }) => {
      const response = requireServerSuccess(
        await getMultiKeyStatus(
          channel.id,
          currentPage,
          10,
          statusFilter ?? undefined,
          signal
        )
      )
      if (!response.data) throw new Error(t('Failed to load key status'))
      return response.data
    },
    gcTime: 0,
  })
  const data = statusQuery.data
  const confirmAction =
    confirmation?.statusSnapshot === data ? confirmation : null
  const setConfirmAction = (action: MultiKeyConfirmAction | null) => {
    setConfirmation(action ? { ...action, statusSnapshot: data } : null)
  }
  const keys = data?.keys ?? []
  const enabledCount = data?.enabled_count ?? 0
  const manualDisabledCount = data?.manual_disabled_count ?? 0
  const autoDisabledCount = data?.auto_disabled_count ?? 0
  const total = enabledCount + manualDisabledCount + autoDisabledCount
  const page = data?.page ?? currentPage
  const totalPages = data?.total_pages ?? 0
  const monitor = data?.balance_monitor
  const busy = isPerformingAction || isUpdatingBalance || statusQuery.isFetching
  const actionsUnavailable = busy || !data || statusQuery.isError
  const balances = new Map(
    monitor?.configuration_changed
      ? []
      : (monitor?.key_balances ?? []).map((balance) => [balance.index, balance])
  )
  const formatBalance = (value: number) =>
    sensitiveVisible
      ? formatCurrencyFromUSD(value, {
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })
      : '••••'

  const performAction = async () => {
    if (!confirmAction || actionsUnavailable) return
    if (
      !canEditSensitive &&
      (confirmAction.type === 'delete' ||
        confirmAction.type === 'delete-disabled')
    ) {
      setConfirmAction(null)
      return
    }
    setIsPerformingAction(true)
    try {
      const { type, keyIndex } = confirmAction
      let response
      if (type === 'enable' && keyIndex !== undefined) {
        response = await enableMultiKey(channel.id, keyIndex)
      } else if (type === 'disable' && keyIndex !== undefined) {
        response = await disableMultiKey(channel.id, keyIndex)
      } else if (type === 'delete' && keyIndex !== undefined) {
        response = await deleteMultiKey(channel.id, keyIndex)
      } else if (type === 'enable-all') {
        response = await enableAllMultiKeys(channel.id)
      } else if (type === 'disable-all') {
        response = await disableAllMultiKeys(channel.id)
      } else if (type === 'delete-disabled') {
        response = await deleteDisabledMultiKeys(channel.id)
      }
      if (!response) return
      requireServerSuccess(response)
      // Refresh related views even if this dialog was closed during the write.
      await refreshChannelStatusQueries(queryClient)
      if (!mounted.current) return
      toast.success(getServerMessage(response) || t('Operation successful'))
      if (
        type.includes('all') ||
        type === 'delete-disabled' ||
        type === 'delete'
      ) {
        setCurrentPage(1)
      }
    } catch (error) {
      if (mounted.current) handleServerError(error, t('Operation failed'))
    } finally {
      if (mounted.current) {
        setIsPerformingAction(false)
        setConfirmAction(null)
      }
    }
  }

  const refreshBalances = async () => {
    if (actionsUnavailable || data?.balance_query_disabled) return
    setIsUpdatingBalance(true)
    try {
      const response = await updateChannelBalance(channel.id)
      // Failed upstream queries also persist a new observation and last-known balance.
      await refreshChannelStatusQueries(queryClient)
      requireServerSuccess(response)
      if (mounted.current) {
        toast.success(getServerMessage(response) || t('Operation successful'))
      }
    } catch (error) {
      if (mounted.current) handleServerError(error, t('Operation failed'))
    } finally {
      if (mounted.current) setIsUpdatingBalance(false)
    }
  }

  return (
    <>
      <Dialog
        open
        onOpenChange={onOpenChange}
        title={
          <>
            {t('Multi-Key Management')}
            <StatusBadge
              label={channel.name}
              variant='neutral'
              copyable={false}
            />
            {channel.channel_info?.multi_key_mode && (
              <StatusBadge
                label={
                  channel.channel_info.multi_key_mode === 'random'
                    ? t('Random')
                    : t('Polling')
                }
                variant='neutral'
                copyable={false}
              />
            )}
          </>
        }
        description={t(
          'Manage multi-key status and configuration for this channel'
        )}
        contentClassName='flex max-h-[min(90dvh,var(--dialog-available-height))] flex-col sm:max-w-6xl'
        titleClassName='flex flex-wrap items-center gap-2'
        contentHeight='min(72vh, 720px)'
        bodyClassName='space-y-4'
      >
        <div className='flex min-h-0 flex-1 flex-col gap-4 overflow-auto'>
          <div className='grid shrink-0 grid-cols-3 gap-3'>
            <StatisticsCard
              label={t('Enabled')}
              count={enabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Manual Disabled')}
              count={manualDisabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Auto Disabled')}
              count={autoDisabledCount}
              total={total}
            />
          </div>
          <Separator className='shrink-0' />
          <div className='flex shrink-0 flex-wrap items-center justify-between gap-2'>
            <Select
              items={MULTI_KEY_FILTER_OPTIONS.map((option) => ({
                value: option.value,
                label: t(option.label),
              }))}
              value={statusFilter === null ? 'all' : statusFilter.toString()}
              disabled={busy}
              onValueChange={(value) => {
                if (value !== null) {
                  setStatusFilter(
                    value === 'all' ? null : Number.parseInt(value)
                  )
                  setCurrentPage(1)
                }
              }}
            >
              <SelectTrigger className='w-40'>
                <SelectValue placeholder={t('All Status')} />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {MULTI_KEY_FILTER_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <div className='flex flex-wrap items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => statusQuery.refetch()}
                disabled={busy}
              >
                <RefreshCw className='size-4' />
                {t('Refresh status')}
              </Button>
              <Button
                variant='outline'
                size='sm'
                onClick={refreshBalances}
                disabled={actionsUnavailable || data?.balance_query_disabled}
              >
                {t('Update Balance')}
              </Button>
              {manualDisabledCount + autoDisabledCount > 0 && (
                <Button
                  size='sm'
                  disabled={actionsUnavailable}
                  onClick={() => setConfirmAction({ type: 'enable-all' })}
                >
                  <Power className='size-4' />
                  {t('Enable All')}
                </Button>
              )}
              {enabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  disabled={actionsUnavailable}
                  onClick={() => setConfirmAction({ type: 'disable-all' })}
                >
                  <PowerOff className='size-4' />
                  {t('Disable All')}
                </Button>
              )}
              {autoDisabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  disabled={actionsUnavailable || !canEditSensitive}
                  onClick={() => setConfirmAction({ type: 'delete-disabled' })}
                  title={
                    canEditSensitive
                      ? undefined
                      : t('No permission to perform this action')
                  }
                >
                  <Trash2 className='size-4' />
                  {t('Delete Auto-Disabled')}
                </Button>
              )}
            </div>
          </div>
          {data?.balance_query_disabled && (
            <Alert>
              <AlertDescription>
                {t(
                  'Balance queries are disabled. Previously recorded balances are retained.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {monitor?.configuration_changed && (
            <Alert>
              <AlertDescription>
                {t(
                  'Channel accounts changed. Refresh balances to see the current accounts.'
                )}
              </AlertDescription>
            </Alert>
          )}
          <FieldDescription className='text-xs'>
            {t(
              'Leave key cost empty to inherit the channel multiplier; 0 means free. User charges are unchanged.'
            )}
          </FieldDescription>
          <div className='min-h-0 flex-1 overflow-auto rounded-md border'>
            {statusQuery.isPending && <LoadingState />}
            {statusQuery.isError && (
              <ErrorState
                title={t('Failed to load key status')}
                onRetry={() => statusQuery.refetch()}
              />
            )}
            {statusQuery.isSuccess && keys.length === 0 && (
              <EmptyState title={t('No keys found')} />
            )}
            {statusQuery.isSuccess && keys.length > 0 && (
              <StaticDataTable
                className='rounded-none border-0'
                tableClassName='min-w-[64rem] table-fixed'
                data={keys}
                getRowKey={(key) => key.index}
                columns={[
                  {
                    id: 'key',
                    header: t('Masked key'),
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => (
                      <div className='space-y-1'>
                        <span className='font-mono text-sm'>
                          #{key.index + 1}
                        </span>
                        <div className='text-muted-foreground font-mono text-xs break-all'>
                          {sensitiveVisible ? key.key_preview || '—' : '••••'}
                        </div>
                      </div>
                    ),
                  },
                  {
                    id: 'status',
                    header: t('Status'),
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => {
                      const config = getMultiKeyStatusConfig(key.status)
                      return (
                        <div className='space-y-1'>
                          <StatusBadge
                            label={t(config.label)}
                            variant={config.variant}
                            showDot
                            copyable={false}
                          />
                          {key.reason && (
                            <p className='text-muted-foreground text-xs wrap-anywhere'>
                              {t(key.reason)}
                            </p>
                          )}
                          {!!key.disabled_time && (
                            <p className='text-muted-foreground text-xs'>
                              {t('Disabled Time')}:{' '}
                              {formatTimestamp(key.disabled_time)}
                            </p>
                          )}
                        </div>
                      )
                    },
                  },
                  {
                    id: 'balance',
                    header: t('Balance'),
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => {
                      const balance = balances.get(key.index)
                      let balanceLabel = balance
                        ? t('Query failed')
                        : t('Not queried')
                      if (balance?.balance != null) {
                        balanceLabel = formatBalance(balance.balance)
                      }
                      return (
                        <div className='space-y-1 text-sm tabular-nums'>
                          <div>{balanceLabel}</div>
                          {balance?.balance == null &&
                            balance?.last_known_balance != null && (
                              <p className='text-muted-foreground text-xs'>
                                {t('Last known balance: {{balance}}', {
                                  balance: formatBalance(
                                    balance.last_known_balance
                                  ),
                                })}
                              </p>
                            )}
                          {balance && !!monitor?.checked_at && (
                            <p className='text-muted-foreground text-xs'>
                              {t('Last queried:')}{' '}
                              {formatTimestamp(monitor.checked_at)}
                            </p>
                          )}
                        </div>
                      )
                    },
                  },
                  {
                    id: 'groups',
                    header: t('Groups'),
                    className: 'w-64',
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => (
                      <KeyGroupsEditor
                        key={`${key.index}:${key.key_identity}`}
                        channelId={channel.id}
                        keyStatus={key}
                        channelGroups={data?.channel_groups ?? []}
                        draft={groupDrafts[`${key.index}:${key.key_identity}`]}
                        onDraftChange={(groups) =>
                          setGroupDrafts((previous) => ({
                            ...previous,
                            [`${key.index}:${key.key_identity}`]: groups,
                          }))
                        }
                        disabled={actionsUnavailable || !canEditSensitive}
                      />
                    ),
                  },
                  {
                    id: 'cost',
                    header: t('Upstream cost multiplier'),
                    className: 'w-44',
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => (
                      <KeyCostEditor
                        key={`${key.index}:${key.key_identity}`}
                        channelId={channel.id}
                        keyStatus={key}
                        channelCostRatio={
                          data?.channel_upstream_cost_ratio ?? null
                        }
                        draft={costDrafts[`${key.index}:${key.key_identity}`]}
                        onDraftChange={(value) =>
                          setCostDrafts((previous) => ({
                            ...previous,
                            [`${key.index}:${key.key_identity}`]: value,
                          }))
                        }
                        disabled={actionsUnavailable || !canEditSensitive}
                      />
                    ),
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    className: 'text-right',
                    cellClassName: 'whitespace-normal align-top',
                    cell: (key) => (
                      <MultiKeyTableRowActions
                        keyIndex={key.index}
                        status={key.status}
                        canDelete={canEditSensitive}
                        disabled={actionsUnavailable}
                        onAction={setConfirmAction}
                      />
                    ),
                  },
                ]}
              />
            )}
          </div>
          {totalPages > 1 && (
            <div className='flex shrink-0 flex-wrap items-center justify-between gap-2'>
              <div className='text-muted-foreground text-sm'>
                {t('Page {{current}} of {{total}}', {
                  current: page,
                  total: totalPages,
                })}
              </div>
              <div className='flex gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => setCurrentPage(page - 1)}
                  disabled={page === 1 || busy}
                >
                  {t('Previous')}
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => setCurrentPage(page + 1)}
                  disabled={page >= totalPages || busy}
                >
                  {t('Next')}
                </Button>
              </div>
            </div>
          )}
        </div>
      </Dialog>
      <ConfirmDialog
        open={confirmAction !== null}
        onOpenChange={(open) =>
          !open && !isPerformingAction && setConfirmAction(null)
        }
        title={t('Confirm Action')}
        desc={t(getMultiKeyConfirmMessage(confirmAction))}
        destructive={isDestructiveAction(confirmAction)}
        isLoading={isPerformingAction}
        disabled={actionsUnavailable}
        handleConfirm={performAction}
      />
    </>
  )
}

function KeyCostEditor(props: {
  channelId: number
  keyStatus: KeyStatus
  channelCostRatio: number | null
  draft?: string
  onDraftChange: (value: string | undefined) => void
  disabled: boolean
}) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const id = useId()
  const savedRatio = props.keyStatus.upstream_cost_ratio ?? null
  const value = props.draft ?? (savedRatio === null ? '' : String(savedRatio))
  const ratio = value.trim() === '' ? null : Number(value)
  const invalid =
    ratio !== null && (!Number.isFinite(ratio) || ratio < 0 || ratio > 10000)
  const changed = !invalid && ratio !== savedRatio
  const save = useMutation({
    mutationFn: async () =>
      requireServerSuccess(
        await setMultiKeyCostRatio(
          props.channelId,
          props.keyStatus.index,
          props.keyStatus.key_identity,
          ratio
        )
      ),
    onSuccess: async () => {
      await refreshChannelStatusQueries(queryClient)
      props.onDraftChange(undefined)
      toast.success(t('Operation successful'))
    },
    onError: async (error) => {
      handleServerError(error, t('Operation failed'))
      await refreshChannelStatusQueries(queryClient)
    },
  })
  const unavailable =
    props.disabled || save.isPending || !props.keyStatus.key_identity
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  // A cost multiplier must retain sub-cent precision; ordinary formatNumber rounds to two decimals.
  const channelCost =
    props.channelCostRatio === null
      ? t('Not configured')
      : new Intl.NumberFormat(locale, { maximumSignificantDigits: 21 }).format(
          props.channelCostRatio
        )
  return (
    <Field
      data-invalid={invalid}
      data-disabled={unavailable}
      className='min-w-0'
    >
      <FieldLabel className='sr-only' htmlFor={id}>
        {t('Upstream cost multiplier for key #{{index}}', {
          index: props.keyStatus.index + 1,
        })}
      </FieldLabel>
      <Input
        id={id}
        inputMode='decimal'
        value={value}
        onChange={(event) => props.onDraftChange(event.target.value)}
        disabled={unavailable}
        aria-invalid={invalid}
        aria-describedby={`${id}-hint`}
        placeholder={t('Inherit channel')}
      />
      <FieldDescription id={`${id}-hint`} className='text-xs'>
        {props.channelCostRatio === null
          ? t('Channel cost is not configured')
          : t('Channel cost: {{ratio}}×', { ratio: channelCost })}
      </FieldDescription>
      {ratio === 0 && (
        <FieldDescription className='text-xs'>
          {t('Free upstream cost')}
        </FieldDescription>
      )}
      {invalid && (
        <FieldError className='text-xs'>
          {t('Upstream cost multiplier must be between 0 and 10000')}
        </FieldError>
      )}
      <Button
        type='button'
        size='sm'
        variant='outline'
        disabled={unavailable || !changed}
        onClick={() => {
          if (!unavailable && changed) save.mutate()
        }}
      >
        {t('Save cost')}
      </Button>
    </Field>
  )
}

function KeyGroupsEditor(props: {
  channelId: number
  keyStatus: KeyStatus
  channelGroups: string[]
  draft?: string[]
  onDraftChange: (groups: string[] | undefined) => void
  disabled: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const id = useId()
  const groups = props.draft ?? props.keyStatus.groups ?? []
  const savedGroups = props.keyStatus.groups ?? []
  const changed =
    groups.length !== savedGroups.length ||
    groups.some((group) => !savedGroups.includes(group))
  const save = useMutation({
    mutationFn: async () =>
      requireServerSuccess(
        await setMultiKeyGroups(
          props.channelId,
          props.keyStatus.index,
          props.keyStatus.key_identity,
          groups
        )
      ),
    onSuccess: async () => {
      await refreshChannelStatusQueries(queryClient)
      props.onDraftChange(undefined)
      toast.success(t('Operation successful'))
    },
    onError: async (error) => {
      handleServerError(error, t('Operation failed'))
      // A failed identity check must refresh the row before another write.
      await refreshChannelStatusQueries(queryClient)
    },
  })
  const unavailable =
    props.disabled || save.isPending || !props.keyStatus.key_identity
  return (
    <div className='min-w-0 space-y-2'>
      <Label className='sr-only' htmlFor={id}>
        {t('Groups for key #{{index}}', { index: props.keyStatus.index + 1 })}
      </Label>
      <MultiSelect
        id={id}
        options={props.channelGroups.map((group) => ({
          value: group,
          label: group,
        }))}
        selected={groups}
        onChange={props.onDraftChange}
        disabled={unavailable}
        maxVisibleChips={2}
        placeholder={t('Groups for key #{{index}}', {
          index: props.keyStatus.index + 1,
        })}
      />
      {groups.length === 0 && (
        <p className='text-muted-foreground text-xs'>
          {t('All channel groups')}
        </p>
      )}
      <Button
        type='button'
        size='sm'
        variant='outline'
        disabled={unavailable || !changed}
        onClick={() => {
          if (!unavailable && changed) save.mutate()
        }}
      >
        {t('Save groups')}
      </Button>
    </div>
  )
}
