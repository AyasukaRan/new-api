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
import { t } from 'i18next'

import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'

import type { ModelRatioData, PricingMode } from './model-pricing-core'

export type ChannelPricing = Record<string, unknown>
export type ChannelPricingMap = Record<string, unknown>

// Keep these limits aligned with setting/ratio_setting/channel_pricing.go.
const MAX_RATIO = 10000
const MAX_PRICE = 1000
const ratioFields = {
  ratio: 'model_ratio',
  completionRatio: 'completion_ratio',
  cacheRatio: 'cache_ratio',
  createCacheRatio: 'create_cache_ratio',
  imageRatio: 'image_ratio',
  audioRatio: 'audio_ratio',
  audioCompletionRatio: 'audio_completion_ratio',
} as const

export function isPricingRecord(
  value: unknown
): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

export function modelChannelOverrides(
  map: ChannelPricingMap,
  modelName: string
): Record<string, ChannelPricing> {
  const model = Object.hasOwn(map, modelName) ? map[modelName] : undefined
  if (model === undefined) return {}
  if (
    !isPricingRecord(model) ||
    Object.values(model).some((node) => !isPricingRecord(node))
  ) {
    throw new Error(t('Invalid channel pricing settings.'))
  }
  return model as Record<string, ChannelPricing>
}

/** Compare a channel node without treating JSON property order as a change. */
export function channelPricingSnapshot(value: unknown): string {
  return (
    JSON.stringify(value, (_key, nested: unknown) => {
      if (!isPricingRecord(nested)) return nested
      return Object.fromEntries(
        Object.entries(nested).sort(([a], [b]) => a.localeCompare(b))
      )
    }) ?? ''
  )
}

/** Resolve legacy modes and nullable lane overrides into the shared price editor. */
export function channelPricingEditorData(
  globalPricing: ModelRatioData,
  override?: ChannelPricing
): ModelRatioData {
  const data = { ...globalPricing }
  const hasEffectivePrice =
    override?.model_price != null || Boolean(globalPricing.price?.trim())
  let mode: PricingMode = hasEffectivePrice ? 'per-request' : 'per-token'
  if (globalPricing.billingMode === 'tiered_expr') mode = 'tiered_expr'

  if (override) {
    for (const [field, stored] of Object.entries(ratioFields)) {
      if (override[stored] != null) {
        data[field as keyof typeof ratioFields] = String(override[stored])
      }
    }
    if (override.model_price != null) data.price = String(override.model_price)
    switch (override.billing_mode) {
      case 'ratio':
        mode = hasEffectivePrice ? 'per-request' : 'per-token'
        break
      case 'per_token':
        mode = 'per-token'
        break
      case 'per_request':
        mode = 'per-request'
        break
      case 'tiered_expr':
        mode = 'tiered_expr'
        Object.assign(
          data,
          splitBillingExprAndRequestRules(String(override.billing_expr ?? ''))
        )
        break
    }
  }

  data.billingMode = mode
  if (mode !== 'per-request') data.price = ''
  if (mode !== 'tiered_expr') {
    data.billingExpr = ''
    data.requestRuleExpr = ''
  }
  return data
}

/** Save an explicit scope while retaining fields owned by newer server versions. */
export function channelPricingFromDraft(
  draft: ModelRatioData,
  original: ChannelPricing = {},
  initialData?: ModelRatioData
): ChannelPricing {
  const pricing = { ...original }
  for (const field of Object.values(ratioFields)) delete pricing[field]
  delete pricing.model_price
  delete pricing.billing_mode
  delete pricing.billing_expr

  let mode = draft.billingMode
  if (!mode) mode = draft.price?.trim() ? 'per-request' : 'per-token'
  if (mode === 'tiered_expr') {
    const expression = combineBillingExpr(
      draft.billingExpr ?? '',
      draft.requestRuleExpr ?? ''
    )
    if (!expression) {
      throw new Error(t('A channel billed by expression needs an expression.'))
    }
    return { ...pricing, billing_mode: 'tiered_expr', billing_expr: expression }
  }
  if (mode === 'per-request') {
    const price = Number(draft.price)
    if (
      !draft.price?.trim() ||
      !Number.isFinite(price) ||
      price < 0 ||
      price > MAX_PRICE
    ) {
      throw new Error(
        t('Price per request must be a number between 0 and {{max}}.', {
          max: MAX_PRICE,
        })
      )
    }
    return { ...pricing, billing_mode: 'per_request', model_price: price }
  }
  if (mode !== 'per-token') {
    throw new Error(t('Unsupported channel billing mode.'))
  }
  if (!draft.ratio?.trim()) {
    throw new Error(t('Input price is required for per-token channel pricing.'))
  }

  pricing.billing_mode = 'per_token'
  for (const [field, stored] of Object.entries(ratioFields)) {
    const value = draft[field as keyof typeof ratioFields]?.trim()
    if (!value) continue
    const ratio = Number(value)
    if (!Number.isFinite(ratio) || ratio < 0 || ratio > MAX_RATIO) {
      throw new Error(
        t('Each ratio must be a number between 0 and {{max}}.', {
          max: MAX_RATIO,
        })
      )
    }
    const initialValue =
      initialData?.[field as keyof typeof ratioFields]?.trim()
    // An inherited lane may use provider-specific billing rather than its
    // displayed fallback multiplier. Only pin it when the operator changes it.
    if (
      original[stored] == null &&
      initialValue &&
      Number(initialValue) === ratio
    ) {
      continue
    }
    pricing[stored] = ratio
  }
  return pricing
}
