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
import {
  render,
  screen,
  waitFor,
  cleanup,
  within,
  act,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { pricingOptions } from '@/features/model-pricing/pricing'
import { ModelPricingEditorPanel } from '@/features/system-settings/models/model-pricing-sheet'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { DataTableRowActions } from '../components/data-table-row-actions'
import { ModelMutateDrawer } from '../components/drawers/model-mutate-drawer'
import { ModelsDialogs } from '../components/models-dialogs'
import { ModelsProvider } from '../components/models-provider'
import type { Model } from '../types'

const model = {
  id: 7,
  model_name: 'example-model',
  description: 'Original',
  status: 1,
  sync_official: 1,
  name_rule: 0,
  vendor_id: 3,
  endpoints: '',
  supported_endpoints: ['openai'],
  created_time: 1,
  updated_time: 1,
}

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
  for (const client of scopedClients.splice(0)) client.clear()
})

function renderModelActions(currentModel: Model = model, role = 100) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelsProvider>
        <DataTableRowActions row={{ original: currentModel } as Row<Model>} />
        <ModelsDialogs />
      </ModelsProvider>
    </QueryClientProvider>
  )
  return client
}

describe('model pricing entry', () => {
  it('saves pricing and opens connections for a channel model without creating metadata', async () => {
    const channelModel = { ...model, id: 0, model_name: 'channel-only' }
    let storedPrice = 1.5
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: 'channel-only',
                  version: 'v1',
                  configured: { ModelPrice: storedPrice },
                  effective: { ModelPrice: storedPrice },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const post = vi.spyOn(api, 'post')
    const put = vi.spyOn(api, 'put')
    const patch = vi.spyOn(api, 'patch').mockImplementation(async () => {
      storedPrice = 0
      return { data: { success: true } }
    })
    const client = renderModelActions(channelModel)
    const user = userEvent.setup()
    expect(screen.getByRole('button', { name: 'Add metadata' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Open menu' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    expect(screen.getByRole('tab', { name: 'Pricing' })).not.toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(
      await screen.findByRole('button', { name: 'Save model prices' })
    ).toBeVisible()
    const price = await screen.findByPlaceholderText('0.01')
    await user.clear(price)
    await user.type(price, '0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'channel-only',
            expected_version: 'v1',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    expect(put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('tab', { name: 'Channels and groups' }))
    expect(
      screen.getByText(
        'Channel availability and group access are derived from enabled channels. Importing metadata does not create a callable channel.'
      )
    ).toBeVisible()
    expect(get).not.toHaveBeenCalledWith('/api/models/0')
    expect(post).not.toHaveBeenCalled()
    client.clear()
  })

  it('opens pricing directly, keeps it selected after metadata loads, and reopens Edit on metadata', async () => {
    let resolveDetail!: (value: Awaited<ReturnType<typeof api.get>>) => void
    const detail = new Promise<Awaited<ReturnType<typeof api.get>>>(
      (resolve) => {
        resolveDetail = resolve
      }
    )
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') return detail
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: model.model_name,
                  version: 'v1',
                  configured: { ModelRatio: 3.25, CompletionRatio: 27 / 6.5 },
                  effective: { ModelRatio: 3.25, CompletionRatio: 27 / 6.5 },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const client = renderModelActions()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    expect(screen.getByRole('tab', { name: 'Pricing' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await act(async () =>
      resolveDetail({ data: { success: true, data: model } })
    )
    expect(screen.getByRole('tab', { name: 'Pricing' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(
      await screen.findByRole('textbox', { name: 'Input price' })
    ).toHaveValue('6.5')
    expect(
      screen.queryByRole('heading', { name: 'Edit model pricing' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('textbox', { name: 'Model name' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: 'Preview' })).toBeVisible()
    for (const tab of ['Model metadata', 'Channels and groups', 'Pricing']) {
      expect(screen.getByRole('tab', { name: tab })).toHaveClass(
        'min-w-0',
        'whitespace-normal'
      )
      await user.click(screen.getByRole('tab', { name: tab }))
      expect(
        screen.getByRole('dialog', { name: model.model_name })
      ).toHaveClass('sm:max-w-[1280px]')
    }
    await user.click(screen.getByRole('button', { name: 'Close' }))
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(await screen.findByLabelText('Description')).toHaveValue('Original')
    expect(screen.getByRole('tab', { name: 'Model metadata' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    client.clear()
  })

  it('does not expose a pricing shortcut to an ordinary administrator', () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [] } },
    })
    const client = renderModelActions(model, 10)
    expect(screen.getByRole('button', { name: 'Edit' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Pricing' })
    ).not.toBeInTheDocument()
    client.clear()
  })

  it('requires a concrete model for matching rules and protects an unsaved price on close', async () => {
    const matchedModel = {
      ...model,
      name_rule: 1,
      matched_models: ['example-concrete'],
    }
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: matchedModel } }
      }
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: 'example-concrete',
                  version: 'v1',
                  configured: { ModelPrice: 1.5 },
                  effective: { ModelPrice: 1.5 },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const client = renderModelActions(matchedModel)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    await user.click(
      await screen.findByRole('combobox', { name: 'Select model' })
    )
    expect(
      get.mock.calls.some(([url]) => url === '/api/option/model_pricing')
    ).toBe(false)
    await user.click(
      await screen.findByRole('option', { name: 'example-concrete' })
    )
    const price = await screen.findByPlaceholderText('0.01')
    await waitFor(() => expect(price).toHaveValue('1.5'))
    await user.clear(price)
    await user.type(price, '2')
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(await screen.findByRole('alertdialog')).toHaveTextContent(
      'Discard unsaved changes?'
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(price).toHaveValue('2')
    client.clear()
  })
})

describe('metadata editing', () => {
  it.each([
    {
      name: 'business rejection',
      response: { success: false, message: '模型名称已存在' },
    },
    { name: 'HTTP rejection', response: null },
  ])(
    'shows the server reason for a $name and preserves the draft for retry',
    async ({ response }) => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'admin', role: 10 })
      vi.spyOn(api, 'get').mockResolvedValue({
        data: { success: true, data: { items: [] } },
      })
      const post = vi.spyOn(api, 'post')
      if (response) {
        post.mockResolvedValueOnce({ data: response })
      } else {
        const error = new AxiosError('Request failed with status code 409')
        error.response = {
          data: { message: '模型名称已存在' },
          status: 409,
          statusText: 'Conflict',
          headers: {},
          config: { headers: {} },
        } as typeof error.response
        post.mockRejectedValueOnce(error)
      }
      post.mockResolvedValue({ data: { success: true } })
      const close = vi.fn()
      const fallbackError = vi.fn()
      const client = new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false, onError: fallbackError },
        },
      })
      render(
        <QueryClientProvider client={client}>
          <ModelMutateDrawer open onOpenChange={close} />
        </QueryClientProvider>
      )
      const user = userEvent.setup()
      await user.type(screen.getByLabelText('Model Name *'), 'duplicate-model')
      await user.type(screen.getByLabelText('Description'), 'Keep this draft')
      await user.click(screen.getByRole('button', { name: 'Save metadata' }))
      expect(await screen.findByRole('alert')).toHaveTextContent(
        '模型名称已存在'
      )
      expect(fallbackError).not.toHaveBeenCalled()
      expect(close).not.toHaveBeenCalled()
      expect(screen.getByLabelText('Model Name *')).toHaveValue(
        'duplicate-model'
      )
      expect(screen.getByLabelText('Description')).toHaveValue(
        'Keep this draft'
      )
      await user.clear(screen.getByLabelText('Model Name *'))
      await user.type(screen.getByLabelText('Model Name *'), 'unique-model')
      await user.click(screen.getByRole('button', { name: 'Save metadata' }))
      await waitFor(() => expect(close).toHaveBeenCalledWith(false))
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(post).toHaveBeenLastCalledWith(
        '/api/models/',
        expect.objectContaining({
          model_name: 'unique-model',
          description: 'Keep this draft',
        }),
        { skipBusinessError: true, skipErrorHandler: true }
      )
      client.clear()
    }
  )

  it('allows an administrator to save metadata without loading or changing system pricing', async () => {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'admin', role: 10 })
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: model } }
      }
      if (url === '/api/vendors/') {
        return {
          data: {
            success: true,
            data: {
              items: [
                { id: 3, name: 'Existing vendor', icon: 'Gemini.Color' },
                { id: 4, name: 'Another vendor', icon: 'Gemini.Color' },
              ],
            },
          },
        }
      }
      return { data: { success: false, message: 'Root only' } }
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: model } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelsProvider>
          <ModelMutateDrawer open onOpenChange={() => {}} currentRow={model} />
        </ModelsProvider>
      </QueryClientProvider>
    )
    const description = await screen.findByLabelText('Description')
    await waitFor(() => expect(description).toHaveValue('Original'))
    expect(screen.getByRole('combobox', { name: 'Vendor' })).toHaveValue(
      'Existing vendor'
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    expect(
      await screen.findByText(
        'Model pricing is managed by a super administrator.'
      )
    ).toBeVisible()
    expect(
      get.mock.calls.some(
        ([url]) =>
          String(url).startsWith('/api/option') ||
          String(url).startsWith('/api/channel')
      )
    ).toBe(false)
    await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
    expect(screen.getByText('Gemini.Color')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Custom model icon' }))
    const icon = screen.getByRole('combobox', { name: 'Icon' })
    await user.type(icon, 'Claude.Avatar')
    await user.keyboard('{Escape}')
    expect(screen.getByText('Claude.Avatar')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Inherit vendor icon' })
    )
    expect(
      screen.queryByRole('combobox', { name: 'Icon' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('Gemini.Color')).toBeVisible()
    const vendorInput = screen.getByRole('combobox', { name: 'Vendor' })
    await user.click(vendorInput)
    await user.type(vendorInput, 'Another')
    await user.click(screen.getByRole('option', { name: 'Another vendor' }))
    expect(vendorInput).toHaveValue('Another vendor')
    await user.clear(screen.getByLabelText('Description'))
    await user.type(screen.getByLabelText('Description'), 'Updated metadata')
    await user.click(
      screen.getByRole('button', { name: /Update Model|Save metadata/ })
    )
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(
      get.mock.calls.some(([url]) => String(url).startsWith('/api/option'))
    ).toBe(false)
    expect(put.mock.calls.every(([url]) => url === '/api/models/')).toBe(true)
    expect(put.mock.calls[0][1]).toMatchObject({
      description: 'Updated metadata',
      icon: '',
      model_name: 'example-model',
      vendor_id: 4,
      endpoints: '',
    })
  })

  it('uses the same price editor for global and channel scopes and preserves failed channel saves', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
    let channelPricing: Record<string, unknown> = {
      [model.model_name]: {
        '7': { billing_mode: 'per_request', model_price: 3 },
      },
      'another-model': { '9': { model_ratio: 4 } },
    }
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: model } }
      }
      if (url === '/api/vendors/') {
        return { data: { success: true, data: { items: [] } } }
      }
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: model.model_name,
                  version: 'v1',
                  configured: { ModelPrice: 1.5 },
                  effective: { ModelPrice: 1.5 },
                },
              ],
              options: pricingOptions({
                ModelPrice: JSON.stringify({ [model.model_name]: 1.5 }),
              }),
              empty_version: 'empty',
            },
          },
        }
      }
      if (url === '/api/option/') {
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
      }
      if (url === '/api/channel') {
        return {
          data: {
            success: true,
            data: {
              items: [
                {
                  id: 7,
                  name: 'Primary channel',
                  status: 1,
                  models: model.model_name,
                },
                {
                  id: 8,
                  name: 'Unrelated channel',
                  status: 1,
                  models: 'different-model',
                },
              ],
              total: 2,
            },
          },
        }
      }
      return { data: { success: true, data: [], vendors: [] } }
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Channel write failed' },
      })
      .mockImplementation(async (_url, request) => {
        channelPricing = JSON.parse((request as { value: string }).value)
        return { data: { success: true } }
      })
    const patch = vi.spyOn(api, 'patch')
    const close = vi.fn()
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelsProvider>
          <ModelMutateDrawer open onOpenChange={close} currentRow={model} />
        </ModelsProvider>
      </QueryClientProvider>
    )
    await waitFor(() =>
      expect(screen.getByLabelText('Description')).toHaveValue('Original')
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    await waitFor(() =>
      expect(screen.getByPlaceholderText('0.01')).toHaveValue('1.5')
    )
    await user.clear(screen.getByPlaceholderText('0.01'))
    await user.type(screen.getByPlaceholderText('0.01'), '2')
    await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
    expect(
      screen.queryByRole('option', { name: /Unrelated channel/ })
    ).not.toBeInTheDocument()
    await user.click(
      await screen.findByRole('option', { name: /Primary channel/ })
    )
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    expect(screen.getByPlaceholderText('0.01')).toHaveValue('2')
    await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
    await user.click(
      await screen.findByRole('option', { name: /Primary channel/ })
    )
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Discard changes',
      })
    )
    await waitFor(() =>
      expect(screen.getByPlaceholderText('0.01')).toHaveValue('3')
    )
    expect(
      screen.queryByRole('button', { name: 'Restore default pricing' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('tab', { name: 'Per-token (deprecated)' })
    ).toBeVisible()
    expect(screen.getByRole('tab', { name: 'Expression' })).toBeVisible()
    expect(
      screen.queryByRole('textbox', { name: 'Model name' })
    ).not.toBeInTheDocument()
    await user.clear(screen.getByPlaceholderText('0.01'))
    await user.type(screen.getByPlaceholderText('0.01'), '4')
    await user.click(
      screen.getByRole('button', { name: 'Save channel pricing' })
    )
    expect(await screen.findByText('Channel write failed')).toBeVisible()
    expect(screen.getByPlaceholderText('0.01')).toHaveValue('4')
    await user.keyboard('{Escape}')
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    expect(close).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Save channel pricing' })
    )
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Save channel pricing' })
      ).toBeDisabled()
    )
    expect(channelPricing).toMatchObject({
      [model.model_name]: {
        '7': { billing_mode: 'per_request', model_price: 4 },
      },
      'another-model': { '9': { model_ratio: 4 } },
    })
    await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
    await user.click(await screen.findByRole('option', { name: 'Global' }))
    await waitFor(() =>
      expect(screen.getByPlaceholderText('0.01')).toHaveValue('1.5')
    )
    expect(patch).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    client.clear()
  })
  it('keeps metadata drafts while saving pricing independently and preserves the price draft across tabs', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
    let version = 'v1'
    let storedPrice = 1.5
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: model } }
      }
      if (url === '/api/vendors/') {
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 3, name: 'Existing vendor', icon: 'Gemini.Color' }],
            },
          },
        }
      }
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: model.model_name,
                  version,
                  configured: { ModelPrice: storedPrice },
                  effective: { ModelPrice: storedPrice },
                },
              ],
              options: pricingOptions({ ModelPrice: '{"example-model":1.5}' }),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: [], vendors: [] } }
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: model } })
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValueOnce({
        data: {
          success: false,
          message: 'Model pricing changed; reload before saving',
        },
      })
      .mockResolvedValue({ data: { success: true } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelsProvider>
          <ModelMutateDrawer open onOpenChange={() => {}} currentRow={model} />
        </ModelsProvider>
      </QueryClientProvider>
    )
    const description = await screen.findByLabelText('Description')
    await waitFor(() => expect(description).toHaveValue('Original'))
    const user = userEvent.setup()
    await user.clear(description)
    await user.type(description, 'Unsaved metadata draft')
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    const price = await screen.findByPlaceholderText('0.01')
    await waitFor(() => expect(price).toHaveValue('1.5'))
    await user.clear(price)
    await user.type(price, '0')
    await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
    expect(screen.getByLabelText('Description')).toHaveValue(
      'Unsaved metadata draft'
    )
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    expect(screen.getByPlaceholderText('0.01')).toHaveValue('0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'example-model',
            expected_version: 'v1',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    version = 'v2'
    storedPrice = 2
    await user.click(
      await screen.findByRole('button', { name: 'Reload pricing' })
    )
    await waitFor(() =>
      expect(screen.getByPlaceholderText('0.01')).toHaveValue('2')
    )
    await user.clear(screen.getByPlaceholderText('0.01'))
    await user.type(screen.getByPlaceholderText('0.01'), '0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenLastCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'example-model',
            expected_version: 'v2',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    expect(put).not.toHaveBeenCalled()
    expect(
      get.mock.calls.some(([url]) => url === '/api/option/model_pricing')
    ).toBe(true)
    await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
    expect(screen.getByLabelText('Description')).toHaveValue(
      'Unsaved metadata draft'
    )
    client.clear()
  })
})

const scopedClients: QueryClient[] = []
function renderScopedPricingEditor(reject = false) {
  let map = { alpha: { '7': { billing_mode: 'per_request', model_price: 3 } } }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/') {
      return {
        data: {
          success: true,
          data: [{ key: 'ChannelModelPricing', value: JSON.stringify(map) }],
        },
      }
    }
    if (url === '/api/channel') {
      return {
        data: {
          success: true,
          data: {
            items: [{ id: 7, name: 'Primary', models: 'alpha', status: 1 }],
            total: 1,
          },
        },
      }
    }
    return { data: { success: true, data: [], vendors: [] } }
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, request) => {
    if (reject) return { data: { success: false, message: 'Write refused' } }
    map = JSON.parse((request as { value: string }).value)
    return { data: { success: true } }
  })
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  scopedClients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ModelPricingEditorPanel
        editData={{ name: 'alpha', price: '1.5', billingMode: 'per-request' }}
      />
    </QueryClientProvider>
  )
  return put
}
async function chooseChannel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
  await user.click(await screen.findByRole('option', { name: /Primary/ }))
  await waitFor(() =>
    expect(screen.getByPlaceholderText('0.01')).toHaveValue('3')
  )
}
it('discard after a failed save reloads unchanged server prices', async () => {
  renderScopedPricingEditor(true)
  const user = userEvent.setup()
  await chooseChannel(user)
  await user.clear(screen.getByPlaceholderText('0.01'))
  await user.type(screen.getByPlaceholderText('0.01'), '4')
  await user.click(screen.getByRole('button', { name: 'Save channel pricing' }))
  await screen.findByText('Write refused')
  await user.click(
    screen.getByRole('button', { name: 'Reload channel pricing' })
  )
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Discard changes',
    })
  )
  await waitFor(() =>
    expect(screen.queryByText('Write refused')).not.toBeInTheDocument()
  )
  expect(screen.getByPlaceholderText('0.01')).toHaveValue('3')
})
it('an equivalent numeric value stays unchanged and the next real edit is protected', async () => {
  const put = renderScopedPricingEditor()
  const user = userEvent.setup()
  await chooseChannel(user)
  await user.clear(screen.getByPlaceholderText('0.01'))
  await user.type(screen.getByPlaceholderText('0.01'), '3.0')
  expect(put).not.toHaveBeenCalled()
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save channel pricing' })
    ).toBeDisabled()
  )
  await user.clear(screen.getByPlaceholderText('0.01'))
  await user.type(screen.getByPlaceholderText('0.01'), '4')
  await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
  await user.click(await screen.findByRole('option', { name: 'Global' }))
  expect(screen.queryByRole('alertdialog')).toBeInTheDocument()
})

it('channel pricing inherits the effective global multiplier and identifies disabled lanes as inherited', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/models/7') return { data: { success: true, data: model } }
    if (url === '/api/vendors/') {
      return { data: { success: true, data: { items: [] } } }
    }
    if (url === '/api/option/model_pricing') {
      return {
        data: {
          success: true,
          data: {
            entries: [
              {
                model_name: model.model_name,
                version: 'v1',
                configured: { ModelRatio: 2, CompletionRatio: 9 },
                effective: { ModelRatio: 2, CompletionRatio: 2 },
              },
            ],
            options: pricingOptions({
              ModelRatio: JSON.stringify({ [model.model_name]: 2 }),
              CompletionRatio: JSON.stringify({ [model.model_name]: 9 }),
            }),
            empty_version: 'empty',
          },
        },
      }
    }
    if (url === '/api/option/') return { data: { success: true, data: [] } }
    if (url === '/api/channel') {
      return {
        data: {
          success: true,
          data: {
            items: [
              { id: 7, name: 'Primary', models: model.model_name, status: 1 },
            ],
            total: 1,
          },
        },
      }
    }
    return { data: { success: true, data: [], vendors: [] } }
  })
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  scopedClients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ModelsProvider>
        <ModelMutateDrawer open onOpenChange={() => {}} currentRow={model} />
      </ModelsProvider>
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await waitFor(() =>
    expect(screen.getByLabelText('Description')).toHaveValue('Original')
  )
  await user.click(screen.getByRole('tab', { name: 'Pricing' }))
  await waitFor(() =>
    expect(screen.getByPlaceholderText('15')).toHaveValue('36')
  )
  await user.click(screen.getByRole('combobox', { name: 'Pricing scope' }))
  await user.click(await screen.findByRole('option', { name: /Primary/ }))
  await waitFor(() =>
    expect(screen.getByPlaceholderText('15')).toHaveValue('8')
  )
  expect(
    screen.queryByRole('button', { name: 'Restore default pricing' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('switch', { name: 'Completion price' }))
  expect(
    screen.getAllByText('Inherits global multiplier').length
  ).toBeGreaterThan(0)
  expect(
    screen.getAllByText(
      'Disabled lanes inherit the global multiplier. Enter 0 for free usage.'
    ).length
  ).toBeGreaterThan(0)
})
