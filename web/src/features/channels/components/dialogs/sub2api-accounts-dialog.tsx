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
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { LogIn, RefreshCw, Unplug } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from '@/components/ui/item'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import {
  getServerErrorMessage,
  translateServerText,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getSub2APIAccounts,
  submitSub2APIAccountAction,
  type Sub2APIAccount,
} from '../../sub2api-api'
import { Sub2APIConnectDialog } from './sub2api-connect-dialog'

export function Sub2APIAccountsDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  if (!props.open) return null
  return <Sub2APIAccountsContent onClose={() => props.onOpenChange(false)} />
}

function Sub2APIAccountsContent(props: { onClose: () => void }) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const canManage = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const client = useQueryClient()
  const [search, setSearch] = useState('')
  const [connect, setConnect] = useState<{
    mode: 'login' | 'import'
    account?: Sub2APIAccount
  } | null>(null)
  const [disconnect, setDisconnect] = useState<Sub2APIAccount | null>(null)
  const [busy, setBusy] = useState(false)
  const operation = useRef<AbortController | null>(null)
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const query = useQuery({
    queryKey: ['sub2api-accounts', user?.id],
    queryFn: ({ signal }) => getSub2APIAccounts(signal),
    gcTime: 0,
    meta: { errorToast: false },
  })
  const mutation = useMutation({
    mutationFn: (request: {
      action: 'refresh' | 'disconnect'
      accountId: number
      proof: string
      signal: AbortSignal
    }) =>
      submitSub2APIAccountAction(
        request.action,
        { account_id: request.accountId },
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
  const status = query.data
  const locked = !canManage || !status?.management_allowed || busy
  const invalidate = () => {
    void client.invalidateQueries({ queryKey: ['sub2api-accounts'] })
    void client.invalidateQueries({ queryKey: ['channel-sub2api'] })
  }
  const runAction = async (
    action: 'refresh' | 'disconnect',
    account: Sub2APIAccount
  ) => {
    if (
      locked ||
      operation.current ||
      (action === 'disconnect' && account.channel_ids.length > 0)
    ) {
      return
    }
    const current = new AbortController()
    operation.current = current
    setBusy(true)
    try {
      const proof = await verification.requestVerification({
        scope: 'sub2api.account.manage',
        context: {
          action,
          account_id: account.id,
          origin: '',
          proxy_encrypted: '',
          email: '',
        },
        title: t('Verify to manage upstream account'),
      })
      if (!proof || operation.current !== current) return
      await mutation.mutateAsync({
        action,
        accountId: account.id,
        proof: proof.proof_token,
        signal: current.signal,
      })
      if (operation.current !== current) return
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
  const accounts = (status?.accounts ?? []).filter((account) =>
    `${account.email} ${account.origin} #${account.id}`
      .toLowerCase()
      .includes(search.trim().toLowerCase())
  )
  return (
    <>
      <Dialog
        open={!connect && !verification.isActive}
        onOpenChange={(open) => {
          if (!open) props.onClose()
        }}
        title={t('Upstream accounts')}
        description={t(
          'Sign in once and link the account to channels on the same site.'
        )}
        contentClassName='sm:max-w-3xl'
      >
        <div className='space-y-4'>
          <div className='flex flex-wrap items-center gap-2'>
            <Input
              className='min-w-0 basis-full sm:flex-1 sm:basis-0'
              aria-label={t('Search accounts')}
              placeholder={t('Search accounts')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
            <Button
              type='button'
              variant='outline'
              disabled={locked || !status?.encryption_ready}
              onClick={() => setConnect({ mode: 'import' })}
            >
              {t('Import tokens')}
            </Button>
            <Button
              type='button'
              disabled={locked || !status?.encryption_ready}
              onClick={() => setConnect({ mode: 'login' })}
            >
              <LogIn />
              {t('Add account')}
            </Button>
          </div>
          {query.isPending && <LoadingState inline size='sm' />}
          {query.isError && (
            <ErrorState
              title={t('Failed to load upstream accounts')}
              description={getServerErrorMessage(query.error)}
              onRetry={() => void query.refetch()}
              className='min-h-0 p-4'
            />
          )}
          {status && !status.management_allowed && (
            <Alert>
              <AlertDescription>
                {t(
                  'Use the HTTPS management address to connect a Sub2API account.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {status?.management_allowed &&
            window.location.protocol !== 'https:' && (
              <p className='text-muted-foreground text-xs'>
                {t('HTTP management is enabled. Use a trusted network.')}
              </p>
            )}
          {status && !status.encryption_ready && (
            <Alert>
              <AlertDescription>
                {t(
                  'Server credential encryption is unavailable. Account connection is disabled.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {status && accounts.length === 0 && (
            <EmptyState
              title={t('No upstream accounts')}
              description={t(
                'Add an account here, then link it from channel settings.'
              )}
              className='min-h-0 py-8'
            />
          )}
          <ItemGroup className='gap-2'>
            {accounts.map((account) => (
              <Item
                key={account.id}
                variant='outline'
                size='sm'
                className='flex-col items-stretch sm:flex-row sm:items-start'
              >
                <ItemContent className='w-full min-w-0 sm:w-auto'>
                  <ItemTitle className='flex min-w-0 flex-wrap items-center gap-2'>
                    <span className='break-all'>{account.email}</span>
                    <span className='text-muted-foreground text-xs'>
                      #{account.id}
                    </span>
                    <StatusBadge
                      label={
                        account.needs_login ? t('Needs login') : t('Connected')
                      }
                      variant={account.needs_login ? 'warning' : 'success'}
                      copyable={false}
                    />
                  </ItemTitle>
                  <ItemDescription className='break-all'>
                    {account.origin}
                  </ItemDescription>
                  <ItemDescription>
                    {account.has_proxy
                      ? t('Proxy configured')
                      : t('Direct connection')}
                    {account.legacy ? ` · ${t('Channel account')}` : ''}
                  </ItemDescription>
                  <ItemDescription>
                    {t('Linked channels')} ({account.channel_ids.length}):{' '}
                    {account.channel_ids.length
                      ? account.channels
                          .map((channel) => `#${channel.id} ${channel.name}`)
                          .join(', ')
                      : t('None')}
                  </ItemDescription>
                  {account.expires_at > 0 && (
                    <ItemDescription>
                      {t('Expires {{time}}', {
                        time: formatTimestampToDate(account.expires_at),
                      })}
                    </ItemDescription>
                  )}
                  {account.last_sync_at > 0 && (
                    <ItemDescription>
                      {t('Last synced')}:{' '}
                      {formatTimestampToDate(account.last_sync_at)}
                    </ItemDescription>
                  )}
                  {account.last_error && (
                    <p className='text-destructive text-xs'>
                      {translateServerText(t, account.last_error)}
                    </p>
                  )}
                  {account.channel_ids.length > 0 && (
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Detach this account from its channels before disconnecting.'
                      )}
                    </p>
                  )}
                </ItemContent>
                <ItemActions className='flex-wrap sm:shrink-0'>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    disabled={locked || !status?.encryption_ready}
                    onClick={() => setConnect({ mode: 'login', account })}
                  >
                    {t('Log in again')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    aria-label={t('Import tokens for {{account}}', {
                      account: account.email,
                    })}
                    disabled={locked || !status?.encryption_ready}
                    onClick={() => setConnect({ mode: 'import', account })}
                  >
                    {t('Import tokens')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon'
                    aria-label={t('Refresh account {{account}}', {
                      account: account.email,
                    })}
                    disabled={
                      locked ||
                      !account.has_refresh_token ||
                      !status?.encryption_ready
                    }
                    onClick={() => void runAction('refresh', account)}
                  >
                    <RefreshCw />
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon'
                    aria-label={t('Disconnect account {{account}}', {
                      account: account.email,
                    })}
                    disabled={locked || account.channel_ids.length > 0}
                    onClick={() => setDisconnect(account)}
                  >
                    <Unplug />
                  </Button>
                </ItemActions>
              </Item>
            ))}
          </ItemGroup>
        </div>
      </Dialog>
      {connect && (
        <Sub2APIConnectDialog
          key={`${connect.account?.id ?? 0}:${connect.mode}`}
          account={connect.account}
          mode={connect.mode}
          managementAllowed={!locked && !!status?.encryption_ready}
          onClose={() => setConnect(null)}
          onDone={invalidate}
        />
      )}
      <ConfirmDialog
        open={disconnect !== null}
        onOpenChange={(open) => {
          if (!open) setDisconnect(null)
        }}
        title={t('Disconnect upstream account')}
        desc={t(
          'Remove this saved login. Existing API keys and historical bills stay unchanged.'
        )}
        confirmText={t('Disconnect')}
        destructive
        handleConfirm={() => {
          const account = disconnect
          setDisconnect(null)
          if (account) void runAction('disconnect', account)
        }}
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}
