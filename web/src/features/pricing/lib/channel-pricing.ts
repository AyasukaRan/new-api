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
import { FILTER_ALL } from '../constants'
import type { PricingModel } from '../types'

/** Keep model metadata, but never inherit price fields from another route. */
export function withChannelPricing(
  model: PricingModel,
  variant: PricingModel
): PricingModel {
  return {
    ...variant,
    model_name: model.model_name,
    group_ratio: model.group_ratio,
    channel_pricing_variants: undefined,
  }
}

export function getChannelPricingVariants(
  model: PricingModel,
  selectedGroup?: string
): PricingModel[] {
  const variants = model.channel_pricing_variants ?? [model]
  return variants
    .filter(
      (variant) =>
        !selectedGroup ||
        selectedGroup === FILTER_ALL ||
        variant.enable_groups.includes(selectedGroup)
    )
    .map((variant) => withChannelPricing(model, variant))
}

/** Observe time rules from all routes, including plugin prices. */
export function getModelBillingExpressions(model: PricingModel): string[] {
  return getChannelPricingVariants(model).flatMap((variant) => [
    variant.billing_expr ?? '',
    ...(variant.billing_plugin_variants?.map((plugin) => plugin.billing_expr) ??
      []),
  ])
}

/** Price identity stays independent of a channel's internal ID and array position. */
export function getChannelPricingKey(model: PricingModel): string {
  return JSON.stringify([
    model.quota_type,
    model.model_ratio,
    model.completion_ratio,
    model.model_price,
    model.cache_ratio,
    model.create_cache_ratio,
    model.image_ratio,
    model.audio_ratio,
    model.audio_completion_ratio,
    model.billing_mode,
    model.billing_expr,
    model.billing_usage_schema,
    model.billing_usage_examples,
    model.billing_plugin_variants,
    model.pricing_configured,
    [...model.enable_groups].sort(),
  ])
}
