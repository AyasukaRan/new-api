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
import { Link2, RefreshCw, Unplug } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from '@/components/ui/item'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import {
  getServerErrorMessage,
  translateServerText,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { channelsQueryKeys } from '../lib'
import {
  getSub2APIStatus,
  submitSub2APIAction,
  type Sub2APIAction,
  type Sub2APIAccount,
} from '../sub2api-api'

export function Sub2APISettings(props: {
  channelId: number | null
  disabled: boolean
  hasUnsavedChanges: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const ratio = new Intl.NumberFormat(locale, { maximumFractionDigits: 8 })
  const id = useId()
  const queryClient = useQueryClient()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const operation = useRef<AbortController | null>(null)
  const [selectedAccount, setSelectedAccount] = useState<string | null>(null)
  const [disconnect, setDisconnect] = useState<Sub2APIAccount | null>(null)
  const [selectedKey, setSelectedKey] = useState('')
  const [busy, setBusy] = useState(false)
  const channelId = props.channelId
  const secureTransport = window.location.protocol === 'https:'
  const query = useQuery({
    queryKey: ['channel-sub2api', userId, channelId],
    queryFn: ({ signal }) => getSub2APIStatus(channelId ?? 0, signal),
    enabled: channelId !== null && !props.hasUnsavedChanges,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const managementAllowed = query.data?.management_allowed === true
  const mutation = useMutation({
    mutationFn: (request: {
      action: Sub2APIAction
      data: Record<string, unknown>
      proof: string
      signal: AbortSignal
    }) =>
      submitSub2APIAction(
        channelId ?? 0,
        request.action,
        request.data,
        request.proof,
        request.signal
      ),
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })

  useEffect(
    () => () => {
      operation.current?.abort()
      operation.current = null
      cancelVerification()
    },
    [cancelVerification]
  )

  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: ['channel-sub2api', userId, channelId],
    })
    if (channelId !== null) {
      void queryClient.invalidateQueries({
        queryKey: channelsQueryKeys.detail(channelId),
      })
    }
    void queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
  }

  const runAction = async (
    action: Sub2APIAction,
    data: Record<string, unknown> = {}
  ) => {
    if (
      channelId === null ||
      !managementAllowed ||
      props.disabled ||
      props.hasUnsavedChanges ||
      operation.current
    ) {
      return
    }
    const current = new AbortController()
    operation.current = current
    setBusy(true)
    try {
      const proof = await verification.requestVerification({
        scope: 'channel.sub2api.manage',
        context: {
          channel_id: channelId,
          action,
          account_id: typeof data.account_id === 'number' ? data.account_id : 0,
          key_identity:
            typeof data.key_identity === 'string' ? data.key_identity : '',
        },
        title: t('Verify to manage upstream account'),
      })
      if (!proof || operation.current !== current) return
      await mutation.mutateAsync({
        action,
        data,
        proof: proof.proof_token,
        signal: current.signal,
      })
      if (operation.current !== current) return
      if (action === 'attach') setSelectedAccount(null)
      invalidate()
      toast.success(t('Saved successfully'))
    } catch (error) {
      if (operation.current === current && !current.signal.aborted) {
        handleServerError(AuthOperationError.from(error))
      }
    } finally {
      mutation.reset()
      if (operation.current === current) {
        operation.current = null
        setBusy(false)
      }
    }
  }

  if (channelId === null) {
    return (
      <Field>
        <FieldLabel>{t('Sub2API account and billing')}</FieldLabel>
        <FieldDescription>
          {t('Save the channel before connecting upstream accounts.')}
        </FieldDescription>
      </Field>
    )
  }
  if (props.hasUnsavedChanges && !query.data) {
    return (
      <section aria-label={t('Sub2API account and billing')}>
        <Field>
          <FieldLabel>{t('Sub2API account and billing')}</FieldLabel>
          <FieldDescription>
            {t('Save channel changes before managing upstream accounts.')}
          </FieldDescription>
        </Field>
      </section>
    )
  }
  if (query.isPending) return <LoadingState inline size='sm' />
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load upstream accounts')}
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
        className='min-h-0 p-4'
      />
    )
  }

  const status = query.data
  const locked =
    !managementAllowed || props.disabled || props.hasUnsavedChanges || busy
  const key =
    status.keys.find((item) => item.key_identity === selectedKey) ??
    status.keys[0]
  const account = status.accounts.find((item) => item.id === key?.account_id)
  const keyItems = status.keys.map((item) => ({
    value: item.key_identity,
    label: `#${item.key_index + 1} ${item.key_preview}`,
  }))

  return (
    <section
      className='space-y-4'
      aria-label={t('Sub2API account and billing')}
      onSubmit={(event) => event.stopPropagation()}
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 className='text-sm font-medium'>
          {t('Sub2API account and billing')}
        </h3>
        <div className='flex flex-wrap gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={locked}
            onClick={() => void runAction('sync')}
          >
            <RefreshCw data-icon='inline-start' />
            {t('Sync now')}
          </Button>
        </div>
      </div>
      {!managementAllowed && (
        <Alert>
          <AlertDescription>
            {t(
              'Use the HTTPS management address to connect a Sub2API account.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {managementAllowed && !secureTransport && (
        <p className='text-muted-foreground text-xs'>
          {t('HTTP management is enabled. Use a trusted network.')}
        </p>
      )}
      {props.hasUnsavedChanges && (
        <p className='text-muted-foreground text-xs'>
          {t('Save channel changes before managing upstream accounts.')}
        </p>
      )}
      {!status.encryption_ready && (
        <Alert>
          <AlertDescription>
            {t(
              'Server credential encryption is unavailable. Account connection is disabled.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <Field>
        <FieldLabel htmlFor={`${id}-account`}>
          {t('Upstream account')}
        </FieldLabel>
        <div className='flex min-w-0 items-center gap-2'>
          <Combobox
            id={`${id}-account`}
            className='min-w-0 flex-1'
            disabled={locked}
            options={status.available_accounts
              .filter(
                (item) => !status.accounts.some((bound) => bound.id === item.id)
              )
              .map((item) => ({
                value: String(item.id),
                label: `${item.email} · #${item.id}`,
              }))}
            value={selectedAccount}
            onValueChange={setSelectedAccount}
            placeholder={t('Select an account')}
            emptyText={t('No available accounts for this site')}
          />
          <Button
            type='button'
            variant='outline'
            disabled={locked || !selectedAccount}
            onClick={() => {
              if (selectedAccount) {
                void runAction('attach', {
                  account_id: Number(selectedAccount),
                })
              }
            }}
          >
            <Link2 />
            {t('Link account')}
          </Button>
        </div>
        <FieldDescription>
          {t(
            'Only accounts on the same site can be linked. Key ownership is verified automatically.'
          )}
        </FieldDescription>
      </Field>
      {status.accounts.length > 0 ? (
        <ItemGroup className='gap-2'>
          {status.accounts.map((item) => (
            <Item key={item.id} variant='outline' size='sm'>
              <ItemContent className='min-w-0'>
                <ItemTitle className='flex min-w-0 flex-wrap items-center gap-2'>
                  <span className='break-all'>{item.email}</span>
                  <StatusBadge
                    label={item.needs_login ? t('Needs login') : t('Connected')}
                    variant={item.needs_login ? 'warning' : 'success'}
                    copyable={false}
                  />
                </ItemTitle>
                <ItemDescription className='break-all'>
                  {item.origin}
                </ItemDescription>
                {item.expires_at > 0 && (
                  <ItemDescription>
                    {t('Expires {{time}}', {
                      time: formatTimestampToDate(item.expires_at),
                    })}
                  </ItemDescription>
                )}
                {item.last_error && (
                  <p className='text-destructive text-xs'>
                    {translateServerText(t, item.last_error)}
                  </p>
                )}
              </ItemContent>
              <ItemActions className='flex-wrap'>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  aria-label={t('Detach account {{account}}', {
                    account: item.email,
                  })}
                  disabled={locked}
                  onClick={() => setDisconnect(item)}
                >
                  <Unplug />
                </Button>
              </ItemActions>
            </Item>
          ))}
        </ItemGroup>
      ) : (
        <p className='text-muted-foreground text-xs'>
          {t('Choose an account saved in Upstream accounts.')}
        </p>
      )}
      {key && (
        <div className='space-y-3 rounded-lg border p-3'>
          <Field>
            <FieldLabel htmlFor={`${id}-key`}>{t('API Key')}</FieldLabel>
            <Select
              value={key.key_identity}
              onValueChange={(value) => {
                if (value) setSelectedKey(value)
              }}
              items={keyItems}
            >
              <SelectTrigger id={`${id}-key`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {keyItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FieldDescription>
              {account ? account.email : t('No matching upstream account')}
            </FieldDescription>
          </Field>
          <Field orientation='horizontal'>
            <FieldLabel htmlFor={`${id}-rates`}>
              {t('Sync upstream key multiplier')}
            </FieldLabel>
            <Switch
              id={`${id}-rates`}
              disabled={locked}
              checked={key.sync_rates}
              onCheckedChange={(checked) =>
                void runAction('keys', {
                  key_identity: key.key_identity,
                  sync_rates: checked,
                  sync_bills: key.sync_bills,
                })
              }
            />
          </Field>
          <Field orientation='horizontal'>
            <FieldLabel htmlFor={`${id}-bills`}>
              {t('Sync actual upstream charges')}
            </FieldLabel>
            <Switch
              id={`${id}-bills`}
              disabled={locked || (!key.account_id && !key.sync_bills)}
              checked={key.sync_bills}
              onCheckedChange={(checked) =>
                void runAction('keys', {
                  key_identity: key.key_identity,
                  sync_rates: key.sync_rates,
                  sync_bills: checked,
                })
              }
            />
          </Field>
          {key.rate && (
            <dl className='grid grid-cols-[1fr_auto] gap-x-3 gap-y-1 text-xs [&_dd]:text-end [&_dd]:tabular-nums'>
              <dt className='text-muted-foreground'>
                {t('Upstream key multiplier')}
              </dt>
              <dd>{ratio.format(key.rate.resolved_rate_multiplier)}×</dd>
              <dt className='text-muted-foreground'>
                {t('Current effective multiplier')}
              </dt>
              <dd>{ratio.format(key.rate.effective_rate_multiplier)}×</dd>
              {key.rate.peak_rate_enabled && (
                <>
                  <dt className='text-muted-foreground'>{t('Peak hours')}</dt>
                  <dd className='break-words'>
                    {key.rate.peak_start}–{key.rate.peak_end}{' '}
                    {key.rate.timezone}
                  </dd>
                </>
              )}
            </dl>
          )}
          {key.last_sync_at > 0 && (
            <p className='text-muted-foreground text-xs'>
              {t('Last rate sync')}: {formatTimestampToDate(key.last_sync_at)}
            </p>
          )}
          {key.sync_bills && (
            <div className='text-muted-foreground space-y-1 text-xs'>
              {typeof key.bills_synced === 'number' &&
                Number.isSafeInteger(key.bills_synced) &&
                key.bills_synced >= 0 &&
                typeof key.bills_pending === 'number' &&
                Number.isSafeInteger(key.bills_pending) &&
                key.bills_pending >= 0 && (
                  <p>
                    {t('{{matched}} matched · {{pending}} pending', {
                      matched: formatNumber(key.bills_synced, locale),
                      pending: formatNumber(key.bills_pending, locale),
                    })}
                  </p>
                )}
              {typeof key.bill_last_synced_at === 'number' &&
                key.bill_last_synced_at > 0 && (
                  <p>
                    {t('Last bill sync')}:{' '}
                    {formatTimestampToDate(key.bill_last_synced_at)}
                  </p>
                )}
            </div>
          )}
          {key.last_error && (
            <p className='text-destructive text-xs'>
              {translateServerText(t, key.last_error)}
            </p>
          )}
          <p className='text-muted-foreground text-xs'>
            {t(
              'Only upstream costs are synced. User prices and recharge multipliers stay unchanged.'
            )}
          </p>
        </div>
      )}
      <ConfirmDialog
        open={disconnect !== null}
        onOpenChange={(open) => {
          if (!open) setDisconnect(null)
        }}
        title={t('Detach upstream account')}
        desc={t(
          'This stops account billing sync only for this channel. Other channels stay connected.'
        )}
        confirmText={t('Detach')}
        destructive
        handleConfirm={() => {
          const target = disconnect
          setDisconnect(null)
          if (target) void runAction('detach', { account_id: target.id })
        }}
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </section>
  )
}
