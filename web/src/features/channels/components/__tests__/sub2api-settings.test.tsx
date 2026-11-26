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
import {
  createDecipheriv,
  generateKeyPairSync,
  privateDecrypt,
} from 'node:crypto'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import { sub2APIImportSchema } from '../../lib/sub2api-login'
import type { Sub2APIStatus } from '../../sub2api-api'
import { Sub2APIAccountsDialog } from '../dialogs/sub2api-accounts-dialog'
import { Sub2APISettings } from '../sub2api-settings'

const { publicKey, privateKey } = generateKeyPairSync('rsa', {
  modulusLength: 2048,
  publicKeyEncoding: { type: 'spki', format: 'pem' },
  privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
})
const url = '/api/channel/7/sub2api'
const accountUrl = '/api/sub2api/accounts'
let status: Sub2APIStatus

beforeEach(() => {
  vi.stubGlobal('location', new URL('https://gateway.example'))
  useAuthStore.getState().auth.setUser({ id: 77, username: 'admin', role: 100 })
  status = {
    encryption_ready: true,
    management_allowed: true,
    available_accounts: [],
    accounts: [
      {
        id: 1,
        origin: 'https://upstream.example',
        email: 'first@example.com',
        upstream_user_id: 19,
        expires_at: 1900000000,
        has_refresh_token: true,
        needs_login: false,
        last_sync_at: 0,
        last_error: '',
        has_proxy: false,
        channel_ids: [],
        channels: [],
        legacy: false,
      },
    ],
    keys: [
      {
        key_identity: 'first-key',
        key_index: 0,
        key_preview: 'sk-…one',
        account_id: 1,
        upstream_api_key_id: 10,
        sync_rates: false,
        sync_bills: false,
        last_sync_at: 0,
        last_error: '',
      },
      {
        key_identity: 'second-key',
        key_index: 1,
        key_preview: 'sk-…two',
        sync_rates: false,
        sync_bills: false,
        last_sync_at: 0,
        last_error: '',
      },
    ],
  }
  vi.spyOn(api, 'get').mockImplementation(async (path, config) => {
    if (path === url || path === accountUrl) {
      return { data: { success: true, data: structuredClone(status) } }
    }
    if (path === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: (config?.params as { scope?: string } | undefined)?.scope,
            methods: [{ method: 'session', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    if (path === '/api/sub2api/accounts/encryption-key') {
      return {
        data: { success: true, data: { kid: 'key-1', public_key: publicKey } },
      }
    }
    throw new Error(`Unexpected GET ${path}`)
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset()
})

function serveActions(
  respond: (
    path: string,
    data: Record<string, unknown>
  ) => unknown | Promise<unknown> = () => ({})
) {
  return vi.spyOn(api, 'post').mockImplementation(async (path, data) => {
    if (path === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'scoped-proof',
            expires_at: Math.floor(Date.now() / 1000) + 60,
            method: 'session',
            scope: (data as { scope: string }).scope,
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: await respond(path, data as Record<string, unknown>),
      },
    }
  })
}

function renderSettings(
  props: Partial<React.ComponentProps<typeof Sub2APISettings>> = {},
  accounts = false
) {
  const onChannelSubmit = vi.fn((event: React.FormEvent) =>
    event.preventDefault()
  )
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <form onSubmit={onChannelSubmit}>
        {accounts ? (
          <Sub2APIAccountsDialog open onOpenChange={() => undefined} />
        ) : (
          <Sub2APISettings
            channelId={7}
            disabled={false}
            hasUnsavedChanges={false}
            {...props}
          />
        )}
      </form>
    </QueryClientProvider>
  )
  return { ...view, client, onChannelSubmit }
}

function renderAccounts() {
  return renderSettings({}, true)
}

test('new channels require saving before connecting accounts, and locked channels cannot mutate credentials', async () => {
  const view = renderSettings({ channelId: null })
  expect(
    screen.getByText('Save the channel before connecting upstream accounts.')
  ).toBeVisible()
  expect(api.get).not.toHaveBeenCalled()
  view.unmount()
  const dirty = renderSettings({ hasUnsavedChanges: true })
  expect(
    screen.getByText('Save channel changes before managing upstream accounts.')
  ).toBeVisible()
  expect(api.get).not.toHaveBeenCalled()
  dirty.unmount()
  renderSettings({ disabled: true })
  expect(
    await screen.findByRole('button', { name: 'Link account' })
  ).toBeDisabled()
  expect(
    screen.getByRole('switch', { name: 'Sync actual upstream charges' })
  ).toHaveAttribute('aria-disabled', 'true')
})

test('login encrypts credentials, completes upstream 2FA, and refreshes the bound account list', async () => {
  const user = userEvent.setup()
  const post = serveActions((path, data) => {
    if (path === `${accountUrl}/login`) {
      return {
        requires_2fa: true,
        flow: 'login-flow',
        email_masked: 's***@example.com',
      }
    }
    if (path === `${accountUrl}/login/2fa`) {
      expect(data).toEqual({ flow: 'login-flow', totp_code: '123456' })
      status.accounts.push({
        ...status.accounts[0],
        id: 2,
        email: 'second@example.com',
      })
      return { requires_2fa: false, account_id: 2 }
    }
    throw new Error(`Unexpected POST ${path}`)
  })
  const { onChannelSubmit } = renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  expect(
    screen.queryByText('HTTP management is enabled. Use a trusted network.')
  ).not.toBeInTheDocument()
  await user.type(
    screen.getByLabelText('Site URL', { exact: false }),
    'https://upstream.example'
  )
  await user.type(screen.getByLabelText(/Email/), 'second@example.com')
  await user.type(screen.getByLabelText(/Password/), 'upstream-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(await screen.findByLabelText(/Authenticator code/), '123456')
  expect(onChannelSubmit).not.toHaveBeenCalled()
  expect(screen.queryByLabelText(/Password/)).not.toBeInTheDocument()
  const loginCall = post.mock.calls.find(
    ([path]) => path === `${accountUrl}/login`
  )
  expect(loginCall?.[1]).toEqual({
    email: 'second@example.com',
    origin: 'https://upstream.example',
    proxy_encrypted: '',
    account_id: 0,
    encryption_key_id: 'key-1',
    password_encrypted: expect.any(String),
  })
  expect(JSON.stringify(loginCall?.[1])).not.toContain('upstream-password')
  expect(loginCall?.[2]).toMatchObject({
    headers: { 'X-Security-Proof': 'scoped-proof' },
    singleUseAuthorization: true,
  })
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'sub2api.account.manage',
      context: {
        action: 'login',
        account_id: 0,
        origin: 'https://upstream.example',
        proxy_encrypted: '',
        email: 'second@example.com',
      },
    }),
    expect.anything()
  )
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(await screen.findByText('second@example.com')).toBeVisible()
  expect(
    screen.queryByRole('dialog', {
      name: /Log in to Sub2API|Import Sub2API tokens/,
    })
  ).not.toBeInTheDocument()
  expect(onChannelSubmit).not.toHaveBeenCalled()
})

test('token import sends one encrypted payload and reopening starts with empty fields', async () => {
  const user = userEvent.setup()
  const post = serveActions(() => ({ requires_2fa: false, account_id: 1 }))
  const { onChannelSubmit } = renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Import tokens' }))
  await user.type(
    screen.getByLabelText('Site URL', { exact: false }),
    'https://upstream.example'
  )
  await user.type(screen.getByLabelText(/Access token/), 'upstream-access')
  await user.type(screen.getByLabelText(/Refresh token/), 'upstream-refresh')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await screen.findByRole('dialog', { name: 'Upstream accounts' })
  const call = post.mock.calls.find(([path]) => path === `${accountUrl}/import`)
  expect(call?.[1]).toEqual({
    credential_encrypted: expect.any(String),
    origin: 'https://upstream.example',
    proxy_encrypted: '',
    account_id: 0,
    encryption_key_id: 'key-1',
  })
  expect(JSON.stringify(call?.[1])).not.toContain('upstream-access')
  expect(JSON.stringify(call?.[1])).not.toContain('upstream-refresh')
  expect(onChannelSubmit).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Import tokens' }))
  expect(screen.getByLabelText(/Access token/)).toHaveValue('')
  expect(screen.getByLabelText(/Refresh token/)).toHaveValue('')
})

test.each(['upstream-access', '  bEaReR upstream-access  '])(
  'imports one access token without a refresh token and normalizes a pasted prefix (%s)',
  async (accessToken) => {
    const user = userEvent.setup()
    const post = serveActions(() => ({ requires_2fa: false, account_id: 1 }))
    renderAccounts()
    await user.click(
      await screen.findByRole('button', { name: 'Import tokens' })
    )
    expect(
      screen.getByText(
        'Refresh token is optional. With only an access token, import it again when it expires.'
      )
    ).toBeVisible()
    await user.type(
      screen.getByLabelText(/Site URL/),
      'https://upstream.example'
    )
    await user.type(screen.getByLabelText(/Access token/), accessToken)
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByRole('dialog', { name: 'Upstream accounts' })
    const call = post.mock.calls.find(
      ([path]) => path === `${accountUrl}/import`
    )
    const payload = call?.[1] as Record<string, string>
    const plaintext = privateDecrypt(
      { key: privateKey, oaepHash: 'sha256' },
      Buffer.from(payload.credential_encrypted, 'base64')
    ).toString()
    expect(JSON.parse(plaintext)).toEqual({ access_token: 'upstream-access' })
    expect(call?.[2]).toMatchObject({
      headers: { 'X-Security-Proof': 'scoped-proof' },
      singleUseAuthorization: true,
    })
  }
)

test('token import rejects multiple tokens and embedded request headers', () => {
  for (const accessToken of [
    'Bearer Bearer upstream-access',
    'upstream-access other-access',
    'Bearer upstream-access\r\nX-Injected: value',
  ]) {
    expect(
      sub2APIImportSchema.safeParse({
        access_token: accessToken,
        refresh_token: '',
      }).success
    ).toBe(false)
  }
})

test('failed token import preserves the site but clears credentials and proxy', async () => {
  const user = userEvent.setup()
  serveActions(() => {
    throw new AuthOperationError(
      'The Sub2API credential is invalid.',
      'SUB2API_CREDENTIAL_INVALID'
    )
  })
  renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Import tokens' }))
  await user.type(
    screen.getByLabelText(/Site URL/),
    'https://upstream.example/prefix'
  )
  await user.type(
    screen.getByLabelText(/Proxy Address/),
    'http://proxy.example:8080'
  )
  await user.type(screen.getByLabelText(/Access token/), 'upstream-access')
  await user.type(screen.getByLabelText(/Refresh token/), 'upstream-refresh')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(
    await screen.findByText('The Sub2API credential is invalid.')
  ).toBeVisible()
  expect(screen.getByLabelText(/Site URL/)).toHaveValue(
    'https://upstream.example/prefix'
  )
  expect(screen.getByLabelText(/Access token/)).toHaveValue('')
  expect(screen.getByLabelText(/Refresh token/)).toHaveValue('')
  expect(screen.getByLabelText(/Proxy Address/)).toHaveValue('')
})

test.each([
  [
    'https://upstream.example',
    'Credential encryption failed. Please try again.',
    'https://upstream.example',
  ],
  [
    'https://example-user:example-password@upstream.example',
    'Enter a valid HTTPS site URL.',
    '',
  ],
])(
  'local import failure clears credentials, retains only a safe site and sends no request (%s)',
  async (origin, message, retainedOrigin) => {
    const respond = vi.mocked(api.get).getMockImplementation()
    if (!respond) throw new Error('Missing GET fixture')
    vi.mocked(api.get).mockImplementation((path, config) => {
      if (path.endsWith('/encryption-key')) {
        return Promise.reject(new Error('Synthetic encryption key failure'))
      }
      return respond(path, config)
    })
    const user = userEvent.setup()
    const post = serveActions()
    renderAccounts()
    await user.click(
      await screen.findByRole('button', { name: 'Import tokens' })
    )
    await user.type(screen.getByLabelText(/Site URL/), origin)
    await user.type(screen.getByLabelText(/Access token/), 'upstream-access')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByText(message)).toBeVisible()
    expect(screen.queryByText('Login failed')).not.toBeInTheDocument()
    expect(screen.getByLabelText(/Site URL/)).toHaveValue(retainedOrigin)
    expect(screen.getByLabelText(/Access token/)).toHaveValue('')
    expect(post).not.toHaveBeenCalled()
  }
)

test('a canceled login ignores its late response and does not reopen a 2FA form', async () => {
  const user = userEvent.setup()
  let finish: (value: unknown) => void = () => undefined
  serveActions((path) =>
    path === `${accountUrl}/login`
      ? new Promise((resolve) => {
          finish = resolve
        })
      : {}
  )
  renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  await user.type(
    screen.getByLabelText('Site URL', { exact: false }),
    'https://upstream.example'
  )
  await user.type(screen.getByLabelText(/Email/), 'second@example.com')
  await user.type(screen.getByLabelText(/Password/), 'upstream-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      `${accountUrl}/login`,
      expect.anything(),
      expect.anything()
    )
  )
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await act(async () =>
    finish({
      requires_2fa: true,
      flow: 'late-flow',
      email_masked: 's***@example.com',
    })
  )
  expect(
    screen.queryByRole('dialog', {
      name: /Log in to Sub2API|Import Sub2API tokens/,
    })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Add account' }))
  expect(screen.getByLabelText(/Password/)).toHaveValue('')
  expect(screen.queryByLabelText(/Authenticator code/)).not.toBeInTheDocument()
})

test('per-key syncing uses stable key identity and does not offer bill sync for an unmatched key', async () => {
  const user = userEvent.setup()
  const post = serveActions()
  const put = vi.spyOn(api, 'put').mockImplementation(async (_path, data) => {
    Object.assign(status.keys[0], data)
    return { data: { success: true, data: {} } }
  })
  renderSettings()
  await user.click(
    await screen.findByRole('switch', { name: 'Sync actual upstream charges' })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      `${url}/keys`,
      { key_identity: 'first-key', sync_rates: false, sync_bills: true },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'scoped-proof' },
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      context: {
        channel_id: 7,
        action: 'keys',
        account_id: 0,
        key_identity: 'first-key',
      },
    }),
    expect.anything()
  )
  await user.click(screen.getByRole('combobox', { name: 'API Key' }))
  await user.click(await screen.findByRole('option', { name: '#2 sk-…two' }))
  expect(screen.getByText('No matching upstream account')).toBeVisible()
  expect(
    screen.getByRole('switch', { name: 'Sync actual upstream charges' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(
    screen.getByRole('switch', { name: 'Sync upstream key multiplier' })
  ).not.toHaveAttribute('aria-disabled', 'true')
})

test('refresh and disconnect authorize the selected account and refresh the status', async () => {
  const user = userEvent.setup()
  const post = serveActions((path) => {
    if (path === `${accountUrl}/disconnect`) {
      status.accounts = []
    }
    return {}
  })
  renderAccounts()
  await user.click(
    await screen.findByRole('button', {
      name: 'Refresh account first@example.com',
    })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      `${accountUrl}/refresh`,
      { account_id: 1 },
      expect.anything()
    )
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', {
        name: 'Disconnect account first@example.com',
      })
    ).toBeEnabled()
  )
  await user.click(
    screen.getByRole('button', { name: 'Disconnect account first@example.com' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Disconnect',
    })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      `${accountUrl}/disconnect`,
      { account_id: 1 },
      expect.objectContaining({ singleUseAuthorization: true })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      context: {
        action: 'disconnect',
        account_id: 1,
        origin: '',
        proxy_encrypted: '',
        email: '',
      },
    }),
    expect.anything()
  )
  await screen.findByText('No upstream accounts')
})

test('failed login preserves the site and email while clearing passwords without creating an account', async () => {
  const user = userEvent.setup()
  const post = serveActions()
  post.mockImplementation(async (path) =>
    path === '/api/verify'
      ? {
          data: {
            success: true,
            data: {
              proof_token: 'scoped-proof',
              expires_at: Math.floor(Date.now() / 1000) + 60,
              method: 'session',
              scope: 'sub2api.account.manage',
            },
          },
        }
      : {
          data: {
            success: false,
            code: 'SUB2API_LOGIN_FAILED',
            message: 'Upstream login failed',
          },
        }
  )
  renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  await user.type(
    screen.getByLabelText('Site URL', { exact: false }),
    'https://upstream.example'
  )
  await user.type(screen.getByLabelText(/Email/), 'second@example.com')
  await user.type(screen.getByLabelText(/Password/), 'wrong-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(await screen.findByText('Upstream login failed')).toBeVisible()
  expect(screen.getByLabelText(/Site URL/)).toHaveValue(
    'https://upstream.example'
  )
  expect(screen.getByLabelText(/Email/)).toHaveValue('second@example.com')
  expect(screen.getByLabelText(/Password/)).toHaveValue('')
  expect(screen.queryByText('second@example.com')).not.toBeInTheDocument()
})

test.each([false, undefined])(
  'HTTP management stays read-only when the server permission is %s',
  async (allowed) => {
    vi.stubGlobal('location', new URL('http://gateway.example'))
    Object.assign(status, { management_allowed: allowed })
    const post = serveActions()
    renderAccounts()
    expect(
      await screen.findByText(
        'Use the HTTPS management address to connect a Sub2API account.'
      )
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Add account' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Import tokens' })).toBeDisabled()
    expect(screen.queryByLabelText(/Password/)).not.toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  }
)

test.each(['login', 'import'] as const)(
  'explicitly allowed HTTP %s keeps proof and real encryption without Web Crypto',
  async (mode) => {
    vi.stubGlobal('location', new URL('http://gateway.example'))
    vi.stubGlobal('crypto', {
      getRandomValues: globalThis.crypto.getRandomValues.bind(
        globalThis.crypto
      ),
    })
    const user = userEvent.setup()
    const post = serveActions(() => ({ requires_2fa: false, account_id: 1 }))
    const { onChannelSubmit } = renderAccounts()
    expect(
      await screen.findByText(
        'HTTP management is enabled. Use a trusted network.'
      )
    ).toBeVisible()
    const open = screen.getByRole('button', {
      name: mode === 'login' ? 'Add account' : 'Import tokens',
    })
    expect(open).toBeEnabled()
    await user.click(open)
    const accessToken = `upstream-access-${'A'.repeat(300)}`
    if (mode === 'login') {
      await user.type(
        screen.getByLabelText('Site URL', { exact: false }),
        'https://upstream.example'
      )
      await user.type(screen.getByLabelText(/Email/), 'first@example.com')
      await user.type(screen.getByLabelText(/Password/), 'upstream-password')
    } else {
      await user.type(
        screen.getByLabelText('Site URL', { exact: false }),
        'https://upstream.example'
      )
      await user.type(screen.getByLabelText(/Access token/), accessToken)
      await user.type(
        screen.getByLabelText(/Refresh token/),
        'upstream-refresh'
      )
    }
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByRole('dialog', { name: 'Upstream accounts' })
    const call = post.mock.calls.find(
      ([path]) => path === `${accountUrl}/${mode}`
    )
    expect(call).toBeDefined()
    expect(call?.[2]).toMatchObject({
      headers: { 'X-Security-Proof': 'scoped-proof' },
      singleUseAuthorization: true,
    })
    const payload = call?.[1] as Record<string, string>
    expect(payload.encryption_key_id).toBe('key-1')
    expect(JSON.stringify(payload)).not.toMatch(
      /upstream-password|upstream-access|upstream-refresh/
    )
    if (mode === 'login') {
      const plaintext = privateDecrypt(
        { key: privateKey, oaepHash: 'sha256' },
        Buffer.from(payload.password_encrypted, 'base64')
      ).toString()
      expect(plaintext).toBe('upstream-password')
    } else {
      const [version, wrapped, nonce, body] =
        payload.credential_encrypted.split('.')
      expect(version).toBe('v2')
      const secret = privateDecrypt(
        {
          key: privateKey,
          oaepHash: 'sha256',
          oaepLabel: Buffer.from('password-v2'),
        },
        Buffer.from(wrapped, 'base64')
      )
      const encrypted = Buffer.from(body, 'base64')
      const decipher = createDecipheriv(
        'aes-256-gcm',
        secret,
        Buffer.from(nonce, 'base64')
      )
      decipher.setAAD(Buffer.from('password-v2:key-1'))
      decipher.setAuthTag(encrypted.subarray(-16))
      const plaintext = Buffer.concat([
        decipher.update(encrypted.subarray(0, -16)),
        decipher.final(),
      ]).toString()
      expect(JSON.parse(plaintext)).toEqual({
        access_token: accessToken,
        refresh_token: 'upstream-refresh',
      })
    }
    expect(onChannelSubmit).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', {
        name: mode === 'login' ? 'Add account' : 'Import tokens',
      })
    )
    expect(
      screen.getByLabelText(mode === 'login' ? /Password/ : /Access token/)
    ).toHaveValue('')
  }
)

test('an expired upstream 2FA flow keeps site and email but returns to an empty password', async () => {
  const user = userEvent.setup()
  serveActions((path) => {
    if (path === `${accountUrl}/login`) {
      return {
        requires_2fa: true,
        flow: 'expired-flow',
        email_masked: 's***@example.com',
      }
    }
    throw new AuthOperationError(
      'The Sub2API verification expired or was already used. Sign in again.',
      'SUB2API_FLOW_EXPIRED'
    )
  })
  renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  await user.type(
    screen.getByLabelText('Site URL', { exact: false }),
    'https://upstream.example'
  )
  await user.type(screen.getByLabelText(/Email/), 'second@example.com')
  await user.type(screen.getByLabelText(/Password/), 'upstream-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(await screen.findByLabelText(/Authenticator code/), '123456')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(
    await screen.findByText(
      'The Sub2API verification expired or was already used. Sign in again.'
    )
  ).toBeVisible()
  expect(screen.getByLabelText(/Site URL/)).toHaveValue(
    'https://upstream.example'
  )
  expect(screen.getByLabelText(/Email/)).toHaveValue('second@example.com')
  expect(screen.getByLabelText(/Password/)).toHaveValue('')
  expect(screen.queryByLabelText(/Authenticator code/)).not.toBeInTheDocument()
})

test('bill matching counts and last bill sync remain distinct from rate sync', async () => {
  Object.assign(status.keys[0], {
    sync_bills: true,
    bills_synced: 12,
    bills_pending: 3,
    bill_last_synced_at: 1795672800,
    last_sync_at: 1795676400,
  })
  renderSettings()
  expect(await screen.findByText('12 matched · 3 pending')).toBeVisible()
  expect(screen.getByText(/^Last bill sync:/)).toBeVisible()
  expect(screen.getByText(/^Last rate sync:/)).toBeVisible()
})

test('channel settings reuse existing accounts without exposing another login form', async () => {
  renderSettings()
  await screen.findByRole('switch', { name: 'Sync upstream key multiplier' })
  expect(
    screen.queryByRole('button', { name: 'Add account' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Import tokens' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('combobox', { name: 'Upstream account' })
  ).toBeVisible()
})

test('a new independent login binds its encrypted proxy to proof before any channel exists', async () => {
  status.accounts = []
  const user = userEvent.setup()
  const post = serveActions(() => ({ requires_2fa: false, account_id: 8 }))
  renderAccounts()
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  await user.type(
    screen.getByLabelText(/Site URL/),
    'https://second.example/prefix'
  )
  await user.type(
    screen.getByLabelText(/Proxy Address/),
    'socks5://proxy-user:proxy-secret@proxy.example:1080'
  )
  await user.type(screen.getByLabelText(/Email/), 'owner@example.com')
  await user.type(screen.getByLabelText(/Password/), 'upstream-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(
      post.mock.calls.some(([path]) => path === `${accountUrl}/login`)
    ).toBe(true)
  )
  const payload = post.mock.calls.find(
    ([path]) => path === `${accountUrl}/login`
  )?.[1] as Record<string, string | number>
  expect(payload).toMatchObject({
    account_id: 0,
    origin: 'https://second.example/prefix',
    proxy_encrypted: expect.any(String),
  })
  expect(JSON.stringify(payload)).not.toContain('proxy-secret')
  expect(
    privateDecrypt(
      { key: privateKey, oaepHash: 'sha256' },
      Buffer.from(String(payload.proxy_encrypted), 'base64')
    ).toString()
  ).toBe('socks5://proxy-user:proxy-secret@proxy.example:1080')
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'sub2api.account.manage',
      context: {
        action: 'login',
        account_id: 0,
        origin: payload.origin,
        proxy_encrypted: payload.proxy_encrypted,
        email: 'owner@example.com',
      },
    }),
    expect.anything()
  )
  expect(
    post.mock.calls.some(([path]) => String(path).startsWith('/api/channel/'))
  ).toBe(false)
})

test('relogin targets the shared account without replacing its site or proxy and blocks disconnect while linked', async () => {
  Object.assign(status.accounts[0], {
    channel_ids: [7, 9],
    channels: [
      { id: 7, name: 'Primary' },
      { id: 9, name: 'Fallback' },
    ],
    needs_login: true,
    has_proxy: true,
  })
  const user = userEvent.setup()
  const post = serveActions(() => ({ requires_2fa: false, account_id: 1 }))
  renderAccounts()
  expect(
    await screen.findByText('#7 Primary, #9 Fallback', { exact: false })
  ).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Disconnect account first@example.com' })
  ).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Log in again' }))
  expect(
    screen.queryByLabelText(/Site URL|Proxy Address|Email/)
  ).not.toBeInTheDocument()
  await user.type(screen.getByLabelText(/Password/), 'replacement-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      `${accountUrl}/login`,
      expect.objectContaining({
        account_id: 1,
        origin: '',
        proxy_encrypted: '',
        email: 'first@example.com',
      }),
      expect.anything()
    )
  )
})

test('linking and detaching an existing account only changes the current channel', async () => {
  status.accounts = []
  status.available_accounts = [
    {
      id: 2,
      origin: 'https://upstream.example',
      email: 'shared@example.com',
      upstream_user_id: 20,
      has_proxy: false,
      channel_ids: [9],
      channels: [{ id: 9, name: 'Other channel' }],
      legacy: false,
      expires_at: 1900000000,
      has_refresh_token: true,
      needs_login: false,
      last_sync_at: 0,
      last_error: '',
    },
  ]
  const user = userEvent.setup()
  const post = serveActions((path) => {
    if (path === `${url}/attach`) {
      status.accounts = [...status.available_accounts]
    }
    if (path === `${url}/detach`) status.accounts = []
    return {}
  })
  renderSettings()
  await user.click(
    await screen.findByRole('combobox', { name: 'Upstream account' })
  )
  await user.click(
    await screen.findByRole('option', { name: 'shared@example.com · #2' })
  )
  await user.click(screen.getByRole('button', { name: 'Link account' }))
  const detach = await screen.findByRole('button', {
    name: 'Detach account shared@example.com',
  })
  await user.click(detach)
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Detach',
    })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      `${url}/detach`,
      { account_id: 2 },
      expect.anything()
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'channel.sub2api.manage',
      context: {
        channel_id: 7,
        action: 'attach',
        account_id: 2,
        key_identity: '',
      },
    }),
    expect.anything()
  )
  expect(
    post.mock.calls.some(([path]) => String(path).startsWith(accountUrl))
  ).toBe(false)
})

test('accounts manager requires channel credential permission before accepting secrets', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 78, username: 'read-only-admin', role: 10 })
  renderAccounts()
  expect(
    await screen.findByRole('button', { name: 'Add account' })
  ).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Import tokens' })).toBeDisabled()
  expect(screen.queryByLabelText(/Password/)).not.toBeInTheDocument()
})
