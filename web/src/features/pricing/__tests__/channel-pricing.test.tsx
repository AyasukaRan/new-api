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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { CachedPriceCell } from '../components/cached-price-cell'
import { ModelCard } from '../components/model-card'
import { ModelPricingDetails } from '../components/model-details'
import { ModelPriceCell } from '../components/model-price-cell'
import { getDynamicPricingSummary } from '../lib/dynamic-price'
import { filterAndSortModels, filterByQuotaType } from '../lib/filters'
import type { PricingModel } from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

function model(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    id: 1,
    model_name: 'channel-model',
    quota_type: 0,
    model_ratio: 99,
    completion_ratio: 9,
    enable_groups: ['default', 'premium'],
    group_ratio: { default: 1, premium: 3 },
    ...overrides,
  }
}
function variantsModel(variants: PricingModel[]): PricingModel {
  return { ...model(), channel_pricing_variants: variants } as PricingModel
}
const token = (price: number, groups = ['default']) =>
  model({
    billing_mode: 'tiered_expr',
    billing_expr: `tier("base", p * ${price} + c * ${price * 4} + cr * ${price / 10})`,
    enable_groups: groups,
  })

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'USD' },
  })
})
afterEach(() => {
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

describe('effective channel prices in the model catalog', () => {
  it('shows the channel expression in cards and tables instead of stale global ratios', () => {
    const value = variantsModel([token(2)])
    const view = render(<ModelCard model={value} onClick={vi.fn()} />)
    expect(screen.getByText('$2')).toBeVisible()
    expect(screen.getByText('$8')).toBeVisible()
    expect(screen.queryByText('$198')).not.toBeInTheDocument()
    view.unmount()
    render(<ModelPriceCell model={value} />)
    expect(screen.getByText('2')).toBeVisible()
    expect(screen.getByText('8')).toBeVisible()
  })

  it('shows comparable channel ranges including cache prices', () => {
    const value = variantsModel([token(2), token(5)])
    const view = render(<ModelCard model={value} onClick={vi.fn()} />)
    expect(screen.getByText('$2 – $5')).toBeVisible()
    expect(screen.getByText('$8 – $20')).toBeVisible()
    view.unmount()
    render(<CachedPriceCell model={value} options={{}} />)
    expect(screen.getByText('$0.2 – $0.5')).toBeVisible()
  })

  it('filters channel prices by group and applies only that channels accessible group ratio', () => {
    const value = variantsModel([token(2), token(5, ['premium'])])
    const view = render(<ModelCard model={value} onClick={vi.fn()} />)
    expect(screen.getByText('$2 – $15')).toBeVisible()
    view.rerender(
      <ModelCard model={value} onClick={vi.fn()} selectedGroup='premium' />
    )
    expect(screen.getByText('$15')).toBeVisible()
    expect(screen.queryByText('$2 – $15')).not.toBeInTheDocument()
  })

  it('does not combine per-request and per-token prices into one range', () => {
    const value = variantsModel([
      token(2),
      model({ quota_type: 1, model_price: 0.05 }),
    ])
    const summary = getDynamicPricingSummary(value, { tokenUnit: 'K' })
    expect(summary?.primaryEntries.map((entry) => entry.unit)).toContain(
      'request'
    )
    expect(
      summary?.primaryEntries.find((entry) => entry.unit === 'request')
        ?.formatted
    ).toBe('$0.05')
    expect(
      summary?.primaryEntries.find((entry) => entry.field === 'inputPrice')
        ?.formatted
    ).toBe('$0.002')
    expect(summary?.isMixedBilling).toBe(true)
  })

  it('distinguishes explicit free channel pricing from missing pricing', () => {
    const free = {
      ...model({ model_ratio: 0, completion_ratio: 1 }),
      pricing_configured: true,
    } as PricingModel
    const missing = {
      ...model({ model_ratio: 0 }),
      pricing_configured: false,
    } as PricingModel
    const view = render(
      <ModelCard model={variantsModel([missing])} onClick={vi.fn()} />
    )
    expect(screen.getByText('Unset price')).toBeVisible()
    expect(screen.queryByText('$0')).not.toBeInTheDocument()
    view.rerender(<ModelCard model={variantsModel([free])} onClick={vi.fn()} />)
    expect(screen.getAllByText('$0')).toHaveLength(2)
  })

  it('sorts by the displayed channel price and filters mixed billing by available modes', () => {
    const cheaper = { ...variantsModel([token(2)]), model_name: 'cheap' }
    const expensive = { ...variantsModel([token(5)]), model_name: 'expensive' }
    const result = filterAndSortModels([expensive, cheaper], {
      search: '',
      vendor: 'all',
      group: 'all',
      quotaType: 'all',
      endpointType: 'all',
      tag: 'all',
      sortBy: 'price-low',
    })
    expect(result.map((item) => item.model_name)).toEqual([
      'cheap',
      'expensive',
    ])
    const mixed = variantsModel([
      token(2),
      model({ quota_type: 1, model_price: 0.05 }),
    ])
    expect(filterByQuotaType([mixed], 'request')).toEqual([mixed])
  })
  it('keeps image-per-item and token options separate in details and supports keyboard selection', async () => {
    const user = userEvent.setup()
    const value = variantsModel([
      model({
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("image", fixed(0.05)) * image_count',
      }),
      model({
        model_ratio: 2.5,
        completion_ratio: 6,
        cache_ratio: 0.25,
        image_ratio: 1.6,
      }),
    ])
    const summary = getDynamicPricingSummary(value, { tokenUnit: 'M' })
    expect(
      summary?.primaryEntries.find((entry) => entry.unit === 'image')?.formatted
    ).toBe('$0.05')
    expect(
      summary?.primaryEntries.find((entry) => entry.field === 'inputPrice')
        ?.formatted
    ).toBe('$5')
    const view = render(
      <ModelPricingDetails
        model={value}
        groupRatio={{ default: 1, premium: 3 }}
        usableGroup={{
          default: { desc: '', ratio: 1 },
          premium: { desc: '', ratio: 3 },
        }}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    )
    expect(
      screen.getByText(
        'The actual price depends on the channel that handles your request.'
      )
    ).toBeVisible()
    const first = screen.getByRole('tab', { name: 'Price option 1' })
    const second = screen.getByRole('tab', { name: 'Price option 2' })
    expect(first).toHaveAttribute('aria-selected', 'true')
    first.focus()
    await user.keyboard('{ArrowRight}{Enter}')
    expect(second).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel')).toHaveTextContent('$30')
    view.rerender(
      <ModelPricingDetails
        model={variantsModel([token(9), token(2)])}
        groupRatio={{ default: 1 }}
        usableGroup={{ default: { desc: '', ratio: 1 } }}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    )
    expect(screen.getByRole('tab', { name: 'Price option 1' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(screen.getByRole('tabpanel')).toHaveTextContent('$36')
    expect(screen.getByRole('tabpanel')).not.toHaveTextContent('$30')
  })

  it('preserves an explicitly free group in channel pricing details', () => {
    render(
      <ModelPricingDetails
        model={variantsModel([token(2)])}
        groupRatio={{ default: 0 }}
        usableGroup={{ default: { desc: '', ratio: 0 } }}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    )
    const groupSection = screen.getByText('Pricing by Group').closest('section')
    if (!groupSection) throw new Error('Expected pricing by group section')
    const table = within(groupSection).getByRole('table')
    expect(within(groupSection).getByText('0x')).toBeVisible()
    expect(table).toHaveTextContent('$0')
    expect(table).not.toHaveTextContent('$2')
  })
})
