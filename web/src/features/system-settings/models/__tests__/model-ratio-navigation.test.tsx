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
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { ModelRatioVisualEditor } from '../model-ratio-visual-editor'

const clients: QueryClient[] = []

async function renderEditor(mobile = false) {
  localStorage.clear()
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'pricing-admin',
    role: ROLE.SUPER_ADMIN,
  })
  const matchMedia = window.matchMedia
  const viewport = new EventTarget()
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...matchMedia(query),
    matches: mobile && query === '(max-width: 767px)',
    addEventListener: viewport.addEventListener.bind(viewport),
    removeEventListener: viewport.removeEventListener.bind(viewport),
  }))
  let channelPricing: Record<string, unknown> = {
    alpha: { '7': { model_ratio: 2 } },
    beta: { '7': { model_ratio: 6 } },
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    switch (url) {
      case '/api/status':
        return { data: { success: true, data: { price: 1 } } }
      case '/api/pricing':
        return { data: { success: true, data: [], vendors: [] } }
      case '/api/option/model_pricing':
        return {
          data: {
            success: true,
            data: {
              entries: ['alpha', 'beta'].map((name) => ({
                model_name: name,
                version: 'v1',
                configured: { ModelRatio: name === 'alpha' ? 1 : 3 },
                effective: {
                  ModelRatio: name === 'alpha' ? 1 : 3,
                  CompletionRatio: 2,
                  CacheRatio: 1,
                  CreateCacheRatio: 1,
                  ImageRatio: 1,
                  AudioRatio: 1,
                  AudioCompletionRatio: 1,
                },
              })),
              options: {},
              empty_version: 'empty',
            },
          },
        }
      case '/api/option/':
        return {
          data: {
            success: true,
            data: [
              {
                key: 'ChannelModelPricing',
                value: JSON.stringify(channelPricing),
              },
            ],
          },
        }
      case '/api/channel':
        return {
          data: {
            success: true,
            data: {
              items: [
                { id: 7, name: 'primary', status: 1, models: 'alpha,beta' },
              ],
              total: 1,
              page: 1,
              page_size: 100,
            },
          },
        }
      default:
        throw new Error(`Unexpected request: ${url}`)
    }
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, request) => {
    channelPricing = JSON.parse((request as { value: string }).value)
    return { data: { success: true } }
  })
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  clients.push(client)
  const modelRatio = JSON.stringify({ alpha: 1, beta: 3 })
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <ModelRatioVisualEditor
          savedModelPrice='{}'
          savedModelRatio={modelRatio}
          savedCacheRatio='{}'
          savedCreateCacheRatio='{}'
          savedCompletionRatio='{}'
          savedImageRatio='{}'
          savedAudioRatio='{}'
          savedAudioCompletionRatio='{}'
          savedBillingMode='{}'
          savedBillingExpr='{}'
          modelPrice='{}'
          modelRatio={modelRatio}
          cacheRatio='{}'
          createCacheRatio='{}'
          completionRatio='{}'
          imageRatio='{}'
          audioRatio='{}'
          audioCompletionRatio='{}'
          billingMode='{}'
          billingExpr='{}'
          onChange={vi.fn()}
          onSave={vi.fn()}
          isSaving={false}
        />
      </QueryClientProvider>
    )
  })
  return {
    put,
    user: userEvent.setup(),
    resize: (nextMobile: boolean) => {
      mobile = nextMobile
      act(() => viewport.dispatchEvent(new Event('change')))
    },
  }
}

afterEach(() => {
  cleanup()
  for (const client of clients.splice(0)) client.clear()
  useAuthStore.getState().auth.reset()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('model pricing editor navigation', () => {
  it('reselecting a model keeps its channel draft and switching models requires an explicit discard', async () => {
    const { user, put } = await renderEditor()
    fireEvent.click(
      within(screen.getByRole('row', { name: /alpha/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    await user.click(
      await screen.findByRole('combobox', { name: 'Pricing scope' })
    )
    await user.click(await screen.findByRole('option', { name: /primary/ }))
    const input = screen.getByPlaceholderText('3')
    await user.clear(input)
    await user.type(input, '8')

    await user.click(
      within(screen.getByRole('row', { name: /alpha/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    expect(input).toHaveValue('8')
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()

    await user.click(
      within(screen.getByRole('row', { name: /beta/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    const confirmation = await screen.findByRole('alertdialog', {
      name: 'Discard unsaved changes?',
    })
    await user.click(
      within(confirmation).getByRole('button', { name: 'Cancel' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(screen.getByPlaceholderText('3')).toHaveValue('8')

    await user.click(
      within(screen.getByRole('row', { name: /beta/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Discard changes',
      })
    )
    await waitFor(() =>
      expect(screen.getByPlaceholderText('3')).toHaveValue('6')
    )
    expect(put).not.toHaveBeenCalled()
    expect(
      screen.getAllByRole('combobox', { name: 'Pricing scope' })
    ).toHaveLength(1)
  })

  it('saving a channel draft permits switching while adding or searching still protects a new unsaved draft', async () => {
    const { user, put } = await renderEditor()
    fireEvent.click(
      within(screen.getByRole('row', { name: /alpha/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    await user.click(
      await screen.findByRole('combobox', { name: 'Pricing scope' })
    )
    await user.click(await screen.findByRole('option', { name: /primary/ }))
    const input = screen.getByPlaceholderText('3')
    await user.clear(input)
    await user.type(input, '8')
    await user.click(
      screen.getByRole('button', { name: 'Save channel pricing' })
    )
    await waitFor(() => expect(put).toHaveBeenCalledOnce())
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Save channel pricing' })
      ).toBeDisabled()
    )

    await user.click(
      within(screen.getByRole('row', { name: /beta/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    await waitFor(() =>
      expect(screen.getByPlaceholderText('3')).toHaveValue('6')
    )
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    const nextInput = screen.getByPlaceholderText('3')
    await user.clear(nextInput)
    await user.type(nextInput, '16')
    await user.click(screen.getByRole('button', { name: 'Add model' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(nextInput).toHaveValue('16')

    await user.type(screen.getByPlaceholderText('Search models...'), 'alpha')
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Discard changes',
      })
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('combobox', { name: 'Pricing scope' })
      ).not.toBeInTheDocument()
    )
    expect(screen.getByText('alpha', { exact: true })).toBeInTheDocument()
    expect(screen.queryByText('beta', { exact: true })).not.toBeInTheDocument()
    expect(put).toHaveBeenCalledOnce()
  })

  it('mobile opens one channel editor and cancelling close preserves its draft until discard is confirmed', async () => {
    const { user, put, resize } = await renderEditor(true)
    fireEvent.click(
      within(screen.getByRole('row', { name: /alpha/ })).getByRole('button', {
        name: 'Edit',
      })
    )
    const sheet = await screen.findByRole('dialog', {
      name: 'Edit model pricing',
    })
    await user.click(
      await within(sheet).findByRole('combobox', { name: 'Pricing scope' })
    )
    await user.click(await screen.findByRole('option', { name: /primary/ }))
    const input = within(sheet).getByPlaceholderText('3')
    expect(
      screen.getAllByRole('combobox', { name: 'Pricing scope', hidden: true })
    ).toHaveLength(1)
    await user.clear(input)
    await user.type(input, '8')

    resize(false)
    expect(screen.getByPlaceholderText('3')).toHaveValue('8')
    expect(
      screen.getAllByRole('combobox', { name: 'Pricing scope', hidden: true })
    ).toHaveLength(1)
    expect(
      screen.getByRole('dialog', { name: 'Edit model pricing' })
    ).toBeVisible()

    await user.click(within(sheet).getByRole('button', { name: 'Close' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(input).toHaveValue('8')
    expect(
      screen.getByRole('dialog', { name: 'Edit model pricing' })
    ).toBeVisible()

    await user.keyboard('{Escape}')
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Discard changes',
      })
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Edit model pricing' })
      ).not.toBeInTheDocument()
    )
    expect(
      screen.queryByRole('combobox', { name: 'Pricing scope' })
    ).not.toBeInTheDocument()
    expect(put).not.toHaveBeenCalled()
  })
})

it('channel pricing keeps omitted lanes inherited and does not use global-only conversion previews', async () => {
  const { user, put } = await renderEditor()
  const preview = vi
    .spyOn(api, 'post')
    .mockResolvedValue({
      data: {
        success: true,
        data: { effective: { ModelRatio: 1, CompletionRatio: 2 } },
      },
    })
  fireEvent.click(
    within(screen.getByRole('row', { name: /alpha/ })).getByRole('button', {
      name: 'Edit',
    })
  )
  expect(
    await screen.findByRole('button', { name: 'Convert to expression' })
  ).toBeVisible()
  await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
  await user.click(await screen.findByRole('option', { name: /primary/ }))
  expect(
    screen.queryByRole('button', { name: 'Convert to expression' })
  ).not.toBeInTheDocument()
  expect(screen.getByPlaceholderText('15')).toHaveValue('8')
  preview.mockClear()
  await user.click(screen.getByRole('switch', { name: 'Completion price' }))
  expect(
    screen.getAllByText('Inherits global multiplier').length
  ).toBeGreaterThan(0)
  await user.click(screen.getByRole('button', { name: 'Save channel pricing' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  const saved = JSON.parse((put.mock.calls[0][1] as { value: string }).value)
  expect(saved.alpha['7']).toMatchObject({
    billing_mode: 'per_token',
    model_ratio: 2,
  })
  expect(saved.alpha['7']).not.toHaveProperty('completion_ratio')
  expect(preview).not.toHaveBeenCalled()
})
