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
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  channelPricingEditorData,
  channelPricingFromDraft,
  type ChannelPricingMap,
} from '../model-channel-pricing'
import type { ModelRatioData } from '../model-pricing-core'
import { useModelChannelPricing } from '../use-model-channel-pricing'

const clients: QueryClient[] = []
const globalPricing: ModelRatioData = {
  name: 'shared-model',
  ratio: '2',
  completionRatio: '3',
  cacheRatio: '0.1',
  createCacheRatio: '1.25',
  imageRatio: '1',
  audioRatio: '4',
  audioCompletionRatio: '2',
}
const expression = 'tier("base", p * 3)'
const requestRule = '(param("service_tier") == "fast" ? 2 : 1)'
type PickerChannel = {
  id: number
  name: string
  models: string
  status: number
  model_mapping?: string
}
type FixtureOptions = {
  optionsSuccess?: boolean
  channelsSuccess?: boolean
  optionError?: boolean
  malformedOptions?: boolean
  pages?: PickerChannel[][]
  total?: number
}

function renderPricing(
  initial: ChannelPricingMap = {},
  options: FixtureOptions = {}
) {
  const state = {
    pricing: initial,
    optionsSuccess: true,
    channelsSuccess: true,
    saveSuccess: true,
    optionError: false,
    malformedOptions: false,
    pages: [
      [{ id: 7, name: 'primary', models: 'shared-model', status: 1 }],
    ] as PickerChannel[][],
    total: 1,
    ...options,
  }
  const get = vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url === '/api/option/') {
      if (state.optionError) throw new Error('Options unavailable')
      return {
        data: {
          success: state.optionsSuccess,
          message: state.optionsSuccess ? '' : 'Options refused',
          data: [
            {
              key: 'ChannelModelPricing',
              value: state.malformedOptions
                ? '[]'
                : JSON.stringify(state.pricing),
            },
          ],
        },
      }
    }
    if (url !== '/api/channel') throw new Error(`Unexpected URL: ${url}`)
    const page = Number(config?.params?.p ?? 1)
    return {
      data: {
        success: state.channelsSuccess,
        message: state.channelsSuccess ? '' : 'Channels refused',
        data: {
          items: state.pages[page - 1] ?? [],
          total: state.total,
          page,
          page_size: 100,
        },
      },
    }
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, request) => {
    if (!state.saveSuccess) {
      return { data: { success: false, message: 'Save refused' } }
    }
    state.pricing = JSON.parse((request as { value: string }).value)
    return { data: { success: true, message: '' } }
  })
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  clients.push(client)
  const hook = renderHook(
    ({ modelName }) => useModelChannelPricing(modelName),
    {
      initialProps: { modelName: 'shared-model' },
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    }
  )
  return { ...hook, state, get, put }
}

afterEach(() => {
  cleanup()
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})

describe('channel pricing conversion', () => {
  it('nullable lanes inherit while zero and relative audio completion keep their values', () => {
    expect(
      channelPricingEditorData(globalPricing, {
        model_ratio: 0,
        completion_ratio: null,
        cache_ratio: 0,
        create_cache_ratio: 2,
        image_ratio: 3,
        audio_ratio: 5,
        audio_completion_ratio: 0,
      })
    ).toEqual({
      ...globalPricing,
      ratio: '0',
      cacheRatio: '0',
      createCacheRatio: '2',
      imageRatio: '3',
      audioRatio: '5',
      audioCompletionRatio: '0',
      billingMode: 'per-token',
      price: '',
      billingExpr: '',
      requestRuleExpr: '',
    })
  })

  it.each([
    {
      global: { price: '0', billingMode: 'per-request' },
      override: { billing_mode: 'per_token' },
      mode: 'per-token',
      price: '',
    },
    {
      global: { billingMode: 'tiered_expr', billingExpr: expression },
      override: { billing_mode: 'per_token' },
      mode: 'per-token',
      price: '',
    },
    {
      global: { billingMode: 'tiered_expr', billingExpr: expression },
      override: { billing_mode: 'per_request', model_price: 0 },
      mode: 'per-request',
      price: '0',
    },
    {
      global: {
        price: '0',
        billingMode: 'tiered_expr',
        billingExpr: expression,
      },
      override: { billing_mode: 'ratio' },
      mode: 'per-request',
      price: '0',
    },
    {
      global: { billingMode: 'tiered_expr', billingExpr: expression },
      override: { billing_mode: 'ratio' },
      mode: 'per-token',
      price: '',
    },
    {
      global: { price: '2' },
      override: { billing_mode: 'per_request', model_price: null },
      mode: 'per-request',
      price: '2',
    },
    {
      global: {},
      override: { model_price: 0 },
      mode: 'per-request',
      price: '0',
    },
    {
      global: { billingMode: 'tiered_expr', billingExpr: expression },
      override: { billing_mode: 'ratio', model_price: 1 },
      mode: 'per-request',
      price: '1',
    },
  ])(
    'resolves $override.billing_mode over global $global.billingMode',
    (row) => {
      expect(
        channelPricingEditorData(
          { ...globalPricing, ...row.global } as ModelRatioData,
          row.override
        )
      ).toMatchObject({
        billingMode: row.mode,
        price: row.price,
        billingExpr: '',
        requestRuleExpr: '',
      })
    }
  )

  it('splits and recombines channel request rules while inherited expressions stay global', () => {
    const inherited = {
      ...globalPricing,
      billingMode: 'tiered_expr' as const,
      billingExpr: expression,
      requestRuleExpr: requestRule,
    }
    expect(
      channelPricingEditorData(inherited, { billing_expr: 'unused' })
    ).toMatchObject({ billingExpr: expression, requestRuleExpr: requestRule })
    const data = channelPricingEditorData(globalPricing, {
      billing_mode: 'tiered_expr',
      billing_expr: `(${expression}) * ${requestRule}`,
    })
    expect(data).toMatchObject({
      billingMode: 'tiered_expr',
      billingExpr: expression,
      requestRuleExpr: requestRule,
    })
    expect(
      channelPricingFromDraft(data, { future: { keep: true }, model_ratio: 2 })
    ).toEqual({
      future: { keep: true },
      billing_mode: 'tiered_expr',
      billing_expr: `(${expression}) * ${requestRule}`,
    })
  })

  it('writes all token lanes and explicit free request prices while removing omitted lanes', () => {
    expect(
      channelPricingFromDraft(
        {
          ...globalPricing,
          ratio: '0',
          cacheRatio: '',
          billingMode: 'per-token',
        },
        {
          billing_mode: 'tiered_expr',
          billing_expr: expression,
          model_price: 3,
          future: 'keep',
        }
      )
    ).toEqual({
      billing_mode: 'per_token',
      model_ratio: 0,
      completion_ratio: 3,
      create_cache_ratio: 1.25,
      image_ratio: 1,
      audio_ratio: 4,
      audio_completion_ratio: 2,
      future: 'keep',
    })
    expect(
      channelPricingFromDraft({
        name: 'shared-model',
        billingMode: 'per-request',
        price: '0',
      })
    ).toEqual({ billing_mode: 'per_request', model_price: 0 })
    expect(
      channelPricingFromDraft({
        name: 'shared-model',
        billingMode: 'per-request',
        price: '1000',
      })
    ).toEqual({ billing_mode: 'per_request', model_price: 1000 })
  })

  it('rejects missing input prices and unsafe numeric values before saving', () => {
    for (const price of ['', '-1', '1000.01', 'Infinity']) {
      expect(() =>
        channelPricingFromDraft({
          name: 'shared-model',
          billingMode: 'per-request',
          price,
        })
      ).toThrow('Price per request must be a number between 0 and 1000.')
    }
    for (const ratio of ['-1', '10001', 'NaN']) {
      expect(() =>
        channelPricingFromDraft({ ...globalPricing, ratio })
      ).toThrow('Each ratio must be a number between 0 and 10000.')
    }
    expect(() =>
      channelPricingFromDraft({
        name: 'shared-model',
        billingMode: 'per-token',
      })
    ).toThrow('Input price is required for per-token channel pricing.')
    expect(() =>
      channelPricingFromDraft({
        name: 'shared-model',
        billingMode: 'tiered_expr',
        billingExpr: '',
      })
    ).toThrow('A channel billed by expression needs an expression.')
  })

  it('keeps unchanged inherited lanes absent, including numerically equivalent audio and cache values', () => {
    const initial = {
      ...globalPricing,
      audioRatio: '1',
      audioCompletionRatio: '0',
    }
    expect(
      channelPricingFromDraft(
        {
          ...initial,
          ratio: '2.0',
          cacheRatio: '.10',
          audioRatio: '1.00',
          audioCompletionRatio: '0.0',
          imageRatio: '0',
          createCacheRatio: '',
        },
        {
          completion_ratio: 3,
          create_cache_ratio: 1.25,
          cache_ratio: null,
          audio_completion_ratio: 0,
          future: 'keep',
        },
        initial
      )
    ).toEqual({
      billing_mode: 'per_token',
      completion_ratio: 3,
      image_ratio: 0,
      audio_completion_ratio: 0,
      future: 'keep',
    })
  })

  it('persists dependent ratios recalculated after the input price changes', () => {
    expect(
      channelPricingFromDraft(
        {
          ...globalPricing,
          ratio: '4',
          completionRatio: '1.5',
          cacheRatio: '.05',
          createCacheRatio: '.625',
          imageRatio: '.5',
          audioRatio: '2',
        },
        {},
        globalPricing
      )
    ).toEqual({
      billing_mode: 'per_token',
      model_ratio: 4,
      completion_ratio: 1.5,
      cache_ratio: 0.05,
      create_cache_ratio: 0.625,
      image_ratio: 0.5,
      audio_ratio: 2,
    })
  })
})

describe('model channel pricing scope', () => {
  it('loads all pages, matches declared models exactly, and retains disabled and unavailable channels', async () => {
    const fixture = renderPricing(
      { 'shared-model': { '99': { model_ratio: 2 } } },
      {
        pages: [
          [
            {
              id: 7,
              name: 'disabled',
              models: 'other,shared-model',
              status: 2,
            },
          ],
          [
            {
              id: 8,
              name: 'substring',
              models: 'shared-model-extra',
              status: 1,
            },
            {
              id: 9,
              name: 'mapping only',
              models: 'other',
              model_mapping: '{"shared-model":"upstream"}',
              status: 1,
            },
            { id: 10, name: 'enabled', models: 'shared-model', status: 1 },
          ],
        ],
        total: 4,
      }
    )
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    expect(fixture.result.current.channelOptions).toEqual([
      { value: '7', label: '#7 disabled · Disabled' },
      { value: '10', label: '#10 enabled · Enabled' },
      { value: '99', label: '#99  · Unavailable channel' },
    ])
    expect(fixture.get).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({ params: expect.objectContaining({ p: 2 }) })
    )
  })

  it('saving a channel merges concurrent siblings and other models and preserves unknown fields', async () => {
    const fixture = renderPricing({
      'shared-model': {
        '7': { model_ratio: 2, future: { keep: true } },
        '9': { model_ratio: 3 },
      },
      other: { '7': { model_ratio: 4 } },
    })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    fixture.state.pricing = {
      'shared-model': {
        '7': { future: { keep: true }, model_ratio: 2 },
        '9': { model_ratio: 8 },
      },
      other: { '7': { model_ratio: 9 } },
    }
    await act(async () =>
      expect(
        await fixture.result.current.save(
          '7',
          {
            ...globalPricing,
            ratio: '0',
            billingMode: 'per-token',
          },
          globalPricing
        )
      ).toBe(true)
    )
    expect(fixture.state.pricing).toEqual({
      'shared-model': {
        '7': {
          future: { keep: true },
          billing_mode: 'per_token',
          model_ratio: 0,
        },
        '9': { model_ratio: 8 },
      },
      other: { '7': { model_ratio: 9 } },
    })
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '0'
    )
  })

  it('a changed target blocks overwrite until reload without replacing the baseline on failure', async () => {
    const fixture = renderPricing({
      'shared-model': { '7': { model_ratio: 2 } },
    })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    const getter = fixture.result.current.getEditorData
    fixture.state.pricing = { 'shared-model': { '7': { model_ratio: 8 } } }
    await act(async () =>
      expect(
        await fixture.result.current.save('7', { ...globalPricing, ratio: '4' })
      ).toBe(false)
    )
    expect(fixture.put).not.toHaveBeenCalled()
    expect(fixture.result.current.conflict).toBe(true)
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '2'
    )
    expect(fixture.result.current.getEditorData).toBe(getter)
    await act(() => fixture.result.current.reload())
    expect(fixture.result.current.conflict).toBe(false)
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '8'
    )
  })

  it('reset removes only the selected channel and the empty model node, including prototype-like names', async () => {
    const fixture = renderPricing(
      JSON.parse(
        '{"__proto__":{"7":{"model_price":0,"billing_mode":"per_request"}},"other":{"9":{"model_ratio":3}}}'
      )
    )
    fixture.rerender({ modelName: '__proto__' })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    expect(fixture.result.current.hasOverride('7')).toBe(true)
    await act(async () =>
      expect(await fixture.result.current.reset('7')).toBe(true)
    )
    expect(fixture.state.pricing).toEqual({
      other: { '9': { model_ratio: 3 } },
    })
    expect(fixture.result.current.hasOverride('7')).toBe(false)
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '2'
    )
  })

  it('a partial reload failure keeps the editing baseline until both reads succeed', async () => {
    const fixture = renderPricing({
      'shared-model': { '7': { model_ratio: 2 } },
    })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    const getter = fixture.result.current.getEditorData
    fixture.state.pricing = { 'shared-model': { '7': { model_ratio: 8 } } }
    fixture.state.channelsSuccess = false
    await act(async () =>
      expect(await fixture.result.current.reload()).toBe(false)
    )
    expect(fixture.result.current.getEditorData).toBe(getter)
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '2'
    )
    expect(fixture.result.current.saveError).toBe('Channels refused')
    fixture.state.channelsSuccess = true
    await act(async () =>
      expect(await fixture.result.current.reload()).toBe(true)
    )
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '8'
    )
    expect(fixture.result.current.saveError).toBe('')
  })

  it('a rejected save preserves baseline and errors do not leak into another model', async () => {
    const fixture = renderPricing({
      'shared-model': { '7': { model_ratio: 2 } },
      beta: { '7': { model_ratio: 6 } },
    })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    fixture.state.saveSuccess = false
    await act(async () =>
      expect(
        await fixture.result.current.save('7', { ...globalPricing, ratio: '4' })
      ).toBe(false)
    )
    expect(fixture.result.current.saveError).toBe('Save refused')
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '2'
    )
    fixture.rerender({ modelName: 'beta' })
    await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
    expect(fixture.result.current.saveError).toBe('')
    expect(fixture.result.current.getEditorData('7', globalPricing).ratio).toBe(
      '6'
    )
  })

  it.each([
    'optionsSuccess',
    'channelsSuccess',
    'optionError',
    'malformedOptions',
  ] as const)(
    'failed loading (%s) prevents replacing the map',
    async (field) => {
      const fixture = renderPricing(
        { 'shared-model': { '7': { model_ratio: 2 } } },
        { [field]: field === 'optionError' || field === 'malformedOptions' }
      )
      await waitFor(() => expect(fixture.result.current.isLoading).toBe(false))
      expect(fixture.result.current.loadError).not.toBe('')
      await act(async () =>
        expect(await fixture.result.current.save('7', globalPricing)).toBe(
          false
        )
      )
      expect(fixture.put).not.toHaveBeenCalled()
    }
  )
})
