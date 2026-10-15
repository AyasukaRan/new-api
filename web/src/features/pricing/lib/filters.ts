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
  SORT_OPTIONS,
  FILTER_ALL,
  QUOTA_TYPES,
  QUOTA_TYPE_VALUES,
  ENDPOINT_TYPES,
  type SortOption,
} from '../constants'
import type { PricingModel } from '../types'
import { getChannelPricingVariants } from './channel-pricing'
import {
  getCardExamplePrice,
  getDynamicPricingSummary,
  hasTaskUsageSchema,
  isUnconfiguredTaskUsageModel,
} from './dynamic-price'
import { getDisplayGroupRatio, isTokenBasedModel } from './model-helpers'
import { calculateTokenPrice } from './price'

// ----------------------------------------------------------------------------
// Filter Utilities
// ----------------------------------------------------------------------------

/**
 * Filter models by search query
 */
export function filterBySearch(
  models: PricingModel[],
  query: string
): PricingModel[] {
  if (!query) return models

  const lowerQuery = query.toLowerCase()
  return models.filter(
    (m) =>
      m.model_name?.toLowerCase().includes(lowerQuery) ||
      m.description?.toLowerCase().includes(lowerQuery) ||
      m.tags?.toLowerCase().includes(lowerQuery) ||
      m.vendor_name?.toLowerCase().includes(lowerQuery)
  )
}

/**
 * Filter models by vendor
 */
export function filterByVendor(
  models: PricingModel[],
  vendor: string
): PricingModel[] {
  if (vendor === FILTER_ALL) return models
  return models.filter((m) => m.vendor_name === vendor)
}

/**
 * Filter models by group
 */
export function filterByGroup(
  models: PricingModel[],
  group: string
): PricingModel[] {
  if (group === FILTER_ALL) return models
  return models.filter((m) => m.enable_groups?.includes(group))
}

/**
 * Filter models by quota type
 */
export function filterByQuotaType(
  models: PricingModel[],
  quotaType: string,
  selectedGroup?: string
): PricingModel[] {
  if (quotaType === QUOTA_TYPES.ALL) return models
  const targetType =
    quotaType === QUOTA_TYPES.TOKEN
      ? QUOTA_TYPE_VALUES.TOKEN
      : QUOTA_TYPE_VALUES.REQUEST
  return models.filter((model) =>
    getChannelPricingVariants(model, selectedGroup).some((variant) => {
      if (quotaType === QUOTA_TYPES.TASK) return hasTaskUsageSchema(variant)
      if (hasTaskUsageSchema(variant)) return false
      const summary = getDynamicPricingSummary(variant, { tokenUnit: 'M' })
      if (summary?.entries.length) {
        return summary.entries.some((entry) =>
          targetType === QUOTA_TYPE_VALUES.TOKEN
            ? entry.unit === 'token'
            : entry.unit === 'request' || entry.unit === 'image'
        )
      }
      return variant.quota_type === targetType
    })
  )
}

/**
 * Filter models by endpoint type
 */
export function filterByEndpointType(
  models: PricingModel[],
  endpointType: string
): PricingModel[] {
  if (endpointType === ENDPOINT_TYPES.ALL) return models
  return models.filter((m) =>
    m.supported_endpoint_types?.includes(endpointType)
  )
}

type SortPriceKind = 'input' | 'output' | 'request'

const PRICE_SORTS: Partial<
  Record<SortOption, { kind: SortPriceKind; dir: 1 | -1 }>
> = {
  [SORT_OPTIONS.INPUT_PRICE_LOW]: { kind: 'input', dir: 1 },
  [SORT_OPTIONS.INPUT_PRICE_HIGH]: { kind: 'input', dir: -1 },
  [SORT_OPTIONS.OUTPUT_PRICE_LOW]: { kind: 'output', dir: 1 },
  [SORT_OPTIONS.OUTPUT_PRICE_HIGH]: { kind: 'output', dir: -1 },
  [SORT_OPTIONS.REQUEST_PRICE_LOW]: { kind: 'request', dir: 1 },
  [SORT_OPTIONS.REQUEST_PRICE_HIGH]: { kind: 'request', dir: -1 },
}

/**
 * Base USD price shown on the model card: input/output per 1M tokens, request
 * per call (task models use their first usage example). Undefined when the
 * model has no price of that kind.
 */
function getSortPrice(
  model: PricingModel,
  kind: SortPriceKind,
  selectedGroup?: string,
  now?: Date
): number | undefined {
  if (
    model.pricing_configured === false &&
    !model.channel_pricing_variants?.length
  ) {
    return undefined
  }
  const ratio = getDisplayGroupRatio(model, selectedGroup)
  const options = {
    tokenUnit: 'M' as const,
    selectedGroup,
    now,
    groupRatioMultiplier: ratio,
  }
  if (kind === 'request') {
    const example = getCardExamplePrice(model, options)
    if (example) {
      const variants = getChannelPricingVariants(model, selectedGroup)
      const exampleRatio =
        variants.length === 1
          ? getDisplayGroupRatio(variants[0], selectedGroup)
          : ratio
      return example.total * exampleRatio
    }
  }
  const summary = getDynamicPricingSummary(model, options)
  if (summary) {
    if (summary.isSpecialExpression) return undefined
    const entry = summary.entries.find((entry) => {
      if (kind === 'request') {
        return (
          (entry.field === 'fixedPrice' || entry.field === 'modelPrice') &&
          entry.unit === 'request'
        )
      }
      return (
        entry.field === (kind === 'input' ? 'inputPrice' : 'outputPrice') &&
        entry.unit === 'token'
      )
    })
    if (!entry) return undefined
    return entry.value * (model.channel_pricing_variants?.length ? 1 : ratio)
  }
  if (isUnconfiguredTaskUsageModel(model)) return undefined
  if (kind === 'request') {
    return !isTokenBasedModel(model) &&
      model.model_price != null &&
      Number.isFinite(model.model_price)
      ? model.model_price * ratio
      : undefined
  }
  if (!isTokenBasedModel(model)) return undefined
  const price = calculateTokenPrice(model, kind, ratio)
  return Number.isFinite(price) ? price : undefined
}

/**
 * Sort models by specified option
 */
export function sortModels(
  models: PricingModel[],
  sortBy: string,
  selectedGroup?: string,
  now?: Date
): PricingModel[] {
  const sorted = [...models]

  // Preserve a saved selection from the former combined price sort.
  const normalizedSort = sortBy.replace(/^price-/, 'input-price-')
  const priceSort = PRICE_SORTS[normalizedSort as SortOption]
  if (priceSort) {
    // Extracting the price parses the billing expression, so compute once
    const price = new Map(
      sorted.map((m) => [
        m,
        getSortPrice(m, priceSort.kind, selectedGroup, now),
      ])
    )
    // Models without this kind of price stay last in both directions
    sorted.sort((a, b) => {
      const priceA = price.get(a)
      const priceB = price.get(b)
      if (priceA === undefined || priceB === undefined) {
        return Number(priceA === undefined) - Number(priceB === undefined)
      }
      return priceSort.dir * (priceA - priceB)
    })
    return sorted
  }

  switch (sortBy) {
    case SORT_OPTIONS.NAME:
      sorted.sort((a, b) =>
        (a.model_name || '').localeCompare(b.model_name || '')
      )
      break
  }

  return sorted
}

/**
 * Apply all filters and sorting to models
 */
export function filterAndSortModels(
  models: PricingModel[],
  filters: {
    search: string
    vendor: string
    group: string
    quotaType: string
    endpointType: string
    tag: string
    sortBy: string
  },
  now?: Date
): PricingModel[] {
  let result = filterBySearch(models, filters.search)
  result = filterByVendor(result, filters.vendor)
  result = filterByGroup(result, filters.group)
  result = filterByQuotaType(result, filters.quotaType, filters.group)
  result = filterByEndpointType(result, filters.endpointType)
  result = filterByTag(result, filters.tag)
  result = sortModels(result, filters.sortBy, filters.group, now)

  return result
}

/**
 * Parse tags from comma-separated string
 */
export function parseTags(tagsString?: string): string[] {
  if (!tagsString) return []
  return tagsString
    .split(/[,;|\s]+/)
    .map((t) => t.trim())
    .filter(Boolean)
}

/**
 * Extract all unique tags from models
 */
export function extractAllTags(models: PricingModel[]): string[] {
  const tagSet = new Set<string>()

  models.forEach((model) => {
    if (model.tags) {
      const tags = parseTags(model.tags)
      tags.forEach((tag) => {
        tagSet.add(tag.toLowerCase())
      })
    }
  })

  return [...tagSet].sort((a, b) => a.localeCompare(b))
}

/**
 * Filter models by tag
 */
export function filterByTag(
  models: PricingModel[],
  tag: string
): PricingModel[] {
  if (tag === FILTER_ALL) return models

  const tagLower = tag.toLowerCase()
  return models.filter((m) => {
    if (!m.tags) return false
    const modelTags = parseTags(m.tags).map((t) => t.toLowerCase())
    return modelTags.includes(tagLower)
  })
}
