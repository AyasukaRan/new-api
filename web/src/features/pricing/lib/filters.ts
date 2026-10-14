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
} from '../constants'
import type { PricingModel } from '../types'
import { getChannelPricingVariants } from './channel-pricing'
import {
  getDynamicPricingSummary,
  hasTaskUsageSchema,
  isUnconfiguredTaskUsageModel,
} from './dynamic-price'
import { getDisplayGroupRatio } from './model-helpers'

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

/**
 * Get model price for sorting
 */
function getModelPrice(
  model: PricingModel,
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
  const summary = getDynamicPricingSummary(model, {
    tokenUnit: 'M',
    selectedGroup: selectedGroup ?? FILTER_ALL,
    now,
    groupRatioMultiplier: ratio,
  })
  if (summary) {
    if (summary.isSpecialExpression) return undefined
    const entry =
      summary.primaryEntries.find((item) => item.field === 'inputPrice') ??
      summary.primaryEntries[0]
    if (!entry) return undefined
    return entry.value * (model.channel_pricing_variants?.length ? 1 : ratio)
  }
  if (isUnconfiguredTaskUsageModel(model)) return undefined
  const price =
    model.quota_type === 0 ? model.model_ratio * 2 : model.model_price
  return typeof price === 'number' && Number.isFinite(price)
    ? price * ratio
    : undefined
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

  switch (sortBy) {
    case SORT_OPTIONS.NAME:
      sorted.sort((a, b) =>
        (a.model_name || '').localeCompare(b.model_name || '')
      )
      break
    case SORT_OPTIONS.PRICE_LOW:
    case SORT_OPTIONS.PRICE_HIGH: {
      const prices = new Map(
        models.map((model) => [model, getModelPrice(model, selectedGroup, now)])
      )
      sorted.sort((a, b) => {
        const left = prices.get(a)
        const right = prices.get(b)
        if (left === undefined) return right === undefined ? 0 : 1
        if (right === undefined) return -1
        return sortBy === SORT_OPTIONS.PRICE_LOW ? left - right : right - left
      })
      break
    }
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
