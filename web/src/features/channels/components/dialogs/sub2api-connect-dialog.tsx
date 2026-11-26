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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ZodError } from 'zod'

import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { encryptChannelCredential } from '@/features/auth/lib/password-encryption'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { AuthOperationError } from '@/lib/secure-verification'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type { PluginLoginField, PluginLoginInput } from '../../lib/plugin-login'
import {
  sub2APIImportSchema,
  sub2APILoginSchema,
  sub2APITotpSchema,
} from '../../lib/sub2api-login'
import {
  submitSub2APIAccountAction,
  type Sub2APIAccount,
  type Sub2APILoginResult,
} from '../../sub2api-api'
import { FieldsStep } from './plugin-login-steps'

export function Sub2APIConnectDialog(props: {
  account?: Sub2APIAccount
  mode: 'login' | 'import'
  managementAllowed: boolean
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const formId = useId()
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const operation = useRef<AbortController | null>(null)
  const [challenge, setChallenge] = useState<{
    flow: string
    email_masked: string
  } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [formVersion, setFormVersion] = useState(0)
  const [nonSensitiveFields, setNonSensitiveFields] = useState({
    origin: '',
    email: '',
  })
  const mutation = useMutation({
    mutationFn: (request: {
      action: 'login' | 'import' | 'login/2fa'
      data: Record<string, unknown>
      proof?: string
      signal: AbortSignal
    }) =>
      submitSub2APIAccountAction<Sub2APILoginResult>(
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

  const submit = async (input: PluginLoginInput) => {
    if (!props.managementAllowed || !('fields' in input) || operation.current) {
      return
    }
    const current = new AbortController()
    operation.current = current
    if (!challenge) {
      setNonSensitiveFields({
        origin: '',
        email: input.fields.email ?? '',
      })
    }
    setBusy(true)
    setError('')
    try {
      let data: Record<string, unknown>
      let proof: string | undefined
      let action: 'login' | 'import' | 'login/2fa' = props.mode
      if (challenge) {
        action = 'login/2fa'
        data = {
          flow: challenge.flow,
          ...sub2APITotpSchema.parse(input.fields),
        }
      } else {
        let origin = ''
        let proxyEncrypted = ''
        let proxyKeyId = ''
        if (!props.account) {
          origin = input.fields.origin?.trim() ?? ''
          let site: URL
          try {
            site = new URL(origin)
          } catch {
            throw new Error(t('Enter a valid HTTPS site URL.'))
          }
          if (
            site.protocol !== 'https:' ||
            site.username ||
            site.password ||
            site.search ||
            site.hash
          ) {
            throw new Error(t('Enter a valid HTTPS site URL.'))
          }
          setNonSensitiveFields({ origin, email: input.fields.email ?? '' })
          if (input.fields.proxy?.trim()) {
            const encryptedProxy = await encryptChannelCredential(
              input.fields.proxy.trim()
            )
            proxyEncrypted = encryptedProxy.password_encrypted
            proxyKeyId = encryptedProxy.encryption_key_id
          }
        }
        if (props.mode === 'login') {
          const credentials = sub2APILoginSchema.parse({
            ...input.fields,
            email: props.account?.email ?? input.fields.email,
          })
          const encrypted = await encryptChannelCredential(credentials.password)
          credentials.password = ''
          data = { email: credentials.email, ...encrypted }
        } else {
          const credentials = sub2APIImportSchema.parse(input.fields)
          const encrypted = await encryptChannelCredential(
            JSON.stringify({
              access_token: credentials.access_token,
              ...(credentials.refresh_token
                ? { refresh_token: credentials.refresh_token }
                : {}),
            })
          )
          credentials.access_token = ''
          credentials.refresh_token = ''
          data = {
            credential_encrypted: encrypted.password_encrypted,
            encryption_key_id: encrypted.encryption_key_id,
          }
        }
        if (proxyKeyId && proxyKeyId !== data.encryption_key_id) {
          throw new Error(t('Operation failed'))
        }
        data = {
          ...data,
          origin,
          proxy_encrypted: proxyEncrypted,
          account_id: props.account?.id ?? 0,
        }
        for (const key of Object.keys(input.fields)) input.fields[key] = ''
        if (operation.current !== current) return
        const result = await verification.requestVerification({
          scope: 'sub2api.account.manage',
          context: {
            action: props.mode,
            account_id: props.account?.id ?? 0,
            origin,
            proxy_encrypted: proxyEncrypted,
            email: props.mode === 'login' ? String(data.email) : '',
          },
          title: t('Verify to manage upstream account'),
        })
        if (!result || operation.current !== current) return
        proof = result.proof_token
      }
      if (operation.current !== current) return
      const result = await mutation.mutateAsync({
        action,
        data,
        proof,
        signal: current.signal,
      })
      if (operation.current !== current) return
      if (result.requires_2fa) {
        setChallenge({ flow: result.flow, email_masked: result.email_masked })
      } else {
        props.onDone()
        props.onClose()
      }
    } catch (failure) {
      if (operation.current === current && !current.signal.aborted) {
        if (failure instanceof ZodError) {
          setError(t(failure.issues[0]?.message ?? 'Invalid parameters'))
        } else {
          const error = AuthOperationError.from(failure)
          if (error.code === 'SUB2API_FLOW_EXPIRED') setChallenge(null)
          setError(getServerErrorMessage(error))
        }
      }
    } finally {
      for (const key of Object.keys(input.fields)) input.fields[key] = ''
      mutation.reset()
      if (operation.current === current) {
        operation.current = null
        setFormVersion((version) => version + 1)
        setBusy(false)
      }
    }
  }

  let fields: PluginLoginField[]
  if (challenge) {
    fields = [
      {
        name: 'totp_code',
        label: t('Authenticator code'),
        type: 'text',
        required: true,
      },
    ]
  } else if (props.mode === 'import') {
    fields = [
      {
        name: 'access_token',
        label: t('Access token'),
        type: 'password',
        required: true,
      },
      {
        name: 'refresh_token',
        label: t('Refresh token'),
        type: 'password',
        description: t(
          'Refresh token is optional. With only an access token, import it again when it expires.'
        ),
      },
    ]
  } else {
    fields = [
      ...(!props.account
        ? [
            {
              name: 'email',
              label: t('Email'),
              type: 'text' as const,
              required: true,
            },
          ]
        : []),
      {
        name: 'password',
        label: t('Password'),
        type: 'password',
        required: true,
      },
    ]
  }

  if (!challenge && !props.account) {
    fields = [
      {
        name: 'origin',
        label: t('Site URL'),
        type: 'text',
        required: true,
        placeholder: 'https://example.com',
      },
      {
        name: 'proxy',
        label: t('Proxy Address'),
        type: 'password',
        description: t('Leave empty for a direct connection.'),
      },
      ...fields,
    ]
  }

  return (
    <>
      <Dialog
        open={!verification.isActive}
        onOpenChange={(open) => {
          if (!open) props.onClose()
        }}
        title={
          props.mode === 'import'
            ? t('Import Sub2API tokens')
            : t('Log in to Sub2API')
        }
        description={
          challenge
            ? t('Enter the code for {{account}}', {
                account: challenge.email_masked,
              })
            : t(
                'Account credentials are stored on the server. Existing API keys stay unchanged.'
              )
        }
        contentClassName='sm:max-w-md'
        footer={
          <>
            <Button type='button' variant='outline' onClick={props.onClose}>
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form={formId}
              disabled={busy || !props.managementAllowed}
            >
              {t('Continue')}
            </Button>
          </>
        }
      >
        <div
          className='space-y-4'
          onSubmit={(event) => event.stopPropagation()}
        >
          {props.account && (
            <p className='text-muted-foreground text-sm break-all'>
              {props.account.email} · {props.account.origin}
            </p>
          )}
          {error && (
            <Alert variant='destructive'>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {!props.managementAllowed && (
            <Alert>
              <AlertDescription>
                {t(
                  'Use the HTTPS management address to connect a Sub2API account.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {props.managementAllowed && busy && <LoadingState inline size='sm' />}
          {props.managementAllowed && !busy && (
            <FieldsStep
              key={formVersion}
              fields={fields}
              defaultValues={nonSensitiveFields}
              formId={formId}
              disabled={false}
              onSubmit={(input) => void submit(input)}
            />
          )}
          {!challenge && props.mode === 'login' && (
            <p className='text-muted-foreground text-xs'>
              {t(
                'If the upstream requires a browser challenge, log in there and import its tokens.'
              )}
            </p>
          )}
        </div>
      </Dialog>
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}
