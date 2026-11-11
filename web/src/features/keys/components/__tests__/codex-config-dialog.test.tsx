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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Row } from '@tanstack/react-table'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, describe, expect, test, vi } from 'vitest'

vi.stubEnv('VITE_BASE_PATH', '/new-api')
const { default: CodexConfigDialog } =
  await import('../dialogs/codex-config-dialog')
const { ApiKeysDialogs } = await import('../api-keys-dialogs')
const { ApiKeysProvider } = await import('../api-keys-provider')
const { DataTableRowActions } = await import('../data-table-row-actions')
const { api } = await import('@/lib/api')
const { useAuthStore } = await import('@/stores/auth-store')

let queryClient: QueryClient

afterAll(() => vi.unstubAllEnvs())

afterEach(() => {
  queryClient?.clear()
  act(() => useAuthStore.getState().auth.reset())
  vi.unstubAllGlobals()
})

function renderDialog(tokenId = 1, tokenKey = 'sk-first-key') {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onOpenChange = vi.fn()
  const tree = (id: number, key: string) => (
    <QueryClientProvider client={queryClient}>
      <CodexConfigDialog
        key={id}
        tokenId={id}
        tokenKey={key}
        onOpenChange={onOpenChange}
      />
    </QueryClientProvider>
  )
  const view = render(tree(tokenId, tokenKey))
  return { ...view, tree, onOpenChange }
}

describe('Codex configuration', () => {
  test('uses this key’s catalog and copies a subpath configuration without the secret', async () => {
    const user = userEvent.setup()
    const clipboard = vi.spyOn(navigator.clipboard, 'writeText')
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          models: [{ slug: 'allowed-model' }, { slug: 'other-model' }],
          data: [{ id: 'not-in-codex-catalog' }],
        })
      )
    )
    vi.stubGlobal('fetch', fetch)
    const store = vi.spyOn(Storage.prototype, 'setItem')
    renderDialog()

    const model = await screen.findByRole('combobox', { name: 'Primary Model' })
    expect(
      screen.queryByRole('textbox', { name: 'config.toml' })
    ).not.toBeInTheDocument()
    await user.click(model)
    await user.type(model, 'allowed-model')
    await user.keyboard('{ArrowDown}{Enter}')
    await user.click(
      await screen.findByRole('button', { name: 'Copy configuration' })
    )

    expect(fetch).toHaveBeenCalledWith(
      `${window.location.origin}/new-api/v1/codex/models`,
      {
        headers: { Authorization: 'Bearer sk-first-key' },
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
        signal: expect.any(AbortSignal),
      }
    )
    const config = clipboard.mock.calls[0]?.[0]
    expect(config).toContain('model = "allowed-model"')
    expect(config).toContain(
      `base_url = "${window.location.origin}/new-api/v1"`
    )
    expect(config).toContain(
      `model_catalog_url = "${window.location.origin}/new-api/v1/codex/models"`
    )
    expect(config).toContain('env_key = "NEWAPI_API_KEY"')
    expect(config).toContain('api_key_model_discovery = true')
    expect(config).not.toContain('sk-first-key')
    expect(
      JSON.stringify(
        queryClient
          .getQueryCache()
          .getAll()
          .map((query) => query.queryKey)
      )
    ).not.toContain('sk-first-key')
    expect(store).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Copy Key' }))
    expect(clipboard).toHaveBeenLastCalledWith('sk-first-key')
  })

  test('escapes quotes and backslashes in the selected model configuration', async () => {
    const user = userEvent.setup()
    const clipboard = vi.spyOn(navigator.clipboard, 'writeText')
    const modelName = 'team/"quoted"\\model'
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify({ models: [{ slug: modelName }] }))
        )
    )
    renderDialog()
    await user.type(
      await screen.findByRole('combobox', { name: 'Primary Model' }),
      modelName
    )
    await user.keyboard('{ArrowDown}{Enter}')
    await user.click(
      await screen.findByRole('button', { name: 'Copy configuration' })
    )
    expect(clipboard.mock.calls[0]?.[0].split('\n')[0]).toBe(
      'model = "team/\\"quoted\\"\\\\model"'
    )
  })

  test('shows an empty state when the selected key has no Codex models', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('{"models":[]}'))
    )
    renderDialog()
    expect(
      await screen.findByText('No models available for this API key')
    ).toBeVisible()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Copy configuration' })
    ).not.toBeInTheDocument()
  })

  test.each([
    new Response('{"error":"unauthorized"}', { status: 401 }),
    new Response('{"data":[{"id":"openai-only"}]}'),
  ])(
    'shows a retryable error for rejected or incompatible catalogs',
    async (response) => {
      const user = userEvent.setup()
      const fetch = vi
        .fn()
        .mockResolvedValueOnce(response)
        .mockResolvedValueOnce(
          new Response('{"models":[{"slug":"recovered-model"}]}')
        )
      vi.stubGlobal('fetch', fetch)
      renderDialog()
      expect(
        await screen.findByText('Failed to load Codex models')
      ).toBeVisible()
      expect(
        screen.queryByRole('button', { name: 'Copy configuration' })
      ).not.toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'Retry' }))
      expect(
        await screen.findByRole('combobox', { name: 'Primary Model' })
      ).toBeEnabled()
    }
  )

  test('loads a fresh catalog when opening another key and releases the old catalog', async () => {
    const user = userEvent.setup()
    let resolveSecond!: (value: Response) => void
    const second = new Promise<Response>((resolve) => {
      resolveSecond = resolve
    })
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response('{"models":[{"slug":"first-model"}]}')
      )
      .mockReturnValueOnce(second)
    vi.stubGlobal('fetch', fetch)
    const view = renderDialog()
    await user.type(
      await screen.findByRole('combobox', { name: 'Primary Model' }),
      'first-model'
    )
    await user.keyboard('{ArrowDown}{Enter}')
    expect(
      await screen.findByRole('button', { name: 'Copy configuration' })
    ).toBeVisible()

    view.rerender(view.tree(2, 'sk-second-key'))
    expect(await screen.findByText('Loading models...')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Copy configuration' })
    ).not.toBeInTheDocument()
    await act(async () =>
      resolveSecond(new Response('{"models":[{"slug":"second-model"}]}'))
    )
    expect(
      await screen.findByRole('combobox', { name: 'Primary Model' })
    ).toHaveValue('')
    await waitFor(() =>
      expect(queryClient.getQueryCache().getAll()).toHaveLength(1)
    )
    expect(fetch.mock.calls[1][1].headers).toEqual({
      Authorization: 'Bearer sk-second-key',
    })
    await user.keyboard('{Escape}')
    expect(view.onOpenChange.mock.calls[0]?.[0]).toBe(false)
  })

  test('cancels a pending catalog request when the dialog is removed', async () => {
    const fetch = vi
      .fn()
      .mockReturnValue(new Promise<Response>(() => undefined))
    vi.stubGlobal('fetch', fetch)
    const view = renderDialog()
    expect(await screen.findByText('Loading models...')).toBeVisible()
    const signal = fetch.mock.calls[0][1].signal as AbortSignal
    view.unmount()
    expect(signal.aborted).toBe(true)
  })

  test('closes configuration and discards the resolved key when the account changes', async () => {
    const user = userEvent.setup()
    const firstUser = { id: 1, username: 'first', role: 1, group: 'default' }
    useAuthStore.getState().auth.setUser(firstUser)
    const fetch = vi
      .fn()
      .mockReturnValue(new Promise<Response>(() => undefined))
    vi.stubGlobal('fetch', fetch)
    const revealKey = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce({
        data: { success: true, data: { key: 'first-secret' } },
      })
      .mockResolvedValueOnce({
        data: { success: true, data: { key: 'second-secret' } },
      })
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    queryClient.setQueryData(['status'], {})
    const row = {
      original: {
        id: 1,
        name: 'test-key',
        key: 'masked',
        status: 1,
        remain_quota: 0,
        used_quota: 0,
        unlimited_quota: true,
        expired_time: -1,
        created_time: 1,
        accessed_time: 0,
        model_limits_enabled: false,
      },
    } as Row<unknown>
    render(
      <QueryClientProvider client={queryClient}>
        <ApiKeysProvider>
          <DataTableRowActions row={row} />
          <ApiKeysDialogs />
        </ApiKeysProvider>
      </QueryClientProvider>
    )
    await user.click(screen.getByRole('button', { name: 'Open menu' }))
    await user.click(screen.getByRole('menuitem', { name: 'Configure Codex' }))
    expect(await screen.findByText('Loading models...')).toBeVisible()
    const firstSignal = fetch.mock.calls[0][1].signal as AbortSignal

    act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ ...firstUser, id: 2, username: 'second' })
    )
    expect(
      screen.queryByRole('dialog', { name: 'Configure Codex' })
    ).not.toBeInTheDocument()
    expect(firstSignal.aborted).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Open menu' }))
    await user.click(screen.getByRole('menuitem', { name: 'Configure Codex' }))
    expect(await screen.findByText('Loading models...')).toBeVisible()
    expect(revealKey).toHaveBeenCalledTimes(2)
    expect(fetch.mock.calls[1][1].headers).toEqual({
      Authorization: 'Bearer sk-second-secret',
    })
  })
})
