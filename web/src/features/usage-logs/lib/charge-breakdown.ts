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
  BILLING_PRICING_VARS,
  normalizeTierLabel,
} from '@/features/pricing/lib/billing-expr'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { formatQuotaWithCurrency } from '@/lib/currency'

import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'
import { getLogCacheUsage } from './cache-usage'
import {
  decodeBillingExprB64,
  getTieredBillingSummary,
  isViolationFeeLog,
  hasAnyCacheTokens,
  getLogCostEstimate,
} from './format'

// A single quota unit is often split across several token or tool lines.
export function formatReceiptQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 9,
    digitsSmall: 9,
    abbreviate: false,
  })
}

export function hasRecordedBillingRates(
  other: LogOtherData,
  isAdmin: boolean,
  type: number
) {
  return Boolean(
    other.billing_mode === 'tiered_expr' ||
    other.model_ratio != null ||
    other.model_price != null ||
    other.group_ratio != null ||
    (other.user_group_ratio != null && other.user_group_ratio !== -1) ||
    (other.usage_facts &&
      typeof other.usage_facts === 'object' &&
      !Array.isArray(other.usage_facts) &&
      Object.keys(other.usage_facts).length) ||
    (other.web_search && other.web_search_call_count) ||
    (other.file_search && other.file_search_call_count) ||
    (other.image_generation_call && other.image_generation_call_price) ||
    (other.audio_input_seperate_price && other.audio_input_price) ||
    (isAdmin &&
      (other.admin_info?.channel_ratio != null ||
        (type === 2 && getLogCostEstimate(other))))
  )
}

export function hasRecordedAudioUsage(other: LogOtherData) {
  return (
    (other.audio || other.ws) &&
    [
      other.audio_input,
      other.audio_output,
      other.text_input,
      other.text_output,
    ].some((count) => (count ?? 0) > 0)
  )
}

// A private accounting marker cannot reveal itself by adding a tab to a personal error.
export function hasLogBillingEvidence(
  other: LogOtherData | null,
  isAdmin: boolean,
  type: number
) {
  if (!other) return false
  return Boolean(
    hasRecordedBillingRates(other, isAdmin, type) ||
    isViolationFeeLog(other) ||
    other.metered_usage ||
    (other.billing_tokens &&
      BILLING_PRICING_VARS.some(({ key }) =>
        Number.isFinite(other.billing_tokens?.[key])
      )) ||
    hasAnyCacheTokens(other) ||
    other.image_cache_tokens != null ||
    (other.image && other.image_output) ||
    hasRecordedAudioUsage(other) ||
    other.billing_source === 'subscription' ||
    other.billing_source === 'free_allowance' ||
    (isAdmin &&
      (other.admin_info?.quota_saturation ||
        other.admin_info?.cost_unconfirmed ||
        other.admin_info?.usage_billing_path ||
        other.admin_info?.local_count_tokens != null))
  )
}

export interface BilledTool {
  name: string
  count: number
  price: number
  per: number
}

export function getBilledTools(other: LogOtherData | null): BilledTool[] {
  if (!other) return []
  const recorded = Array.isArray(other.tool_surcharges)
    ? other.tool_surcharges
        .filter(
          (item) =>
            typeof item?.name === 'string' &&
            item.name.trim() &&
            Number.isSafeInteger(item.count) &&
            item.count > 0 &&
            Number.isFinite(item.price) &&
            item.price > 0
        )
        .map((item) => ({ ...item, per: 1_000 }))
    : []
  if (recorded.length) return recorded
  for (const [name, enabled, count, price, per] of [
    [
      'web_search',
      other.web_search,
      other.web_search_call_count,
      other.web_search_price,
      1_000,
    ],
    [
      'file_search',
      other.file_search,
      other.file_search_call_count,
      other.file_search_price,
      1_000,
    ],
    [
      'image_generation',
      other.image_generation_call,
      other.image_generation_call_count ?? 1,
      other.image_generation_call_price,
      1,
    ],
  ] as const) {
    if (
      enabled &&
      typeof count === 'number' &&
      Number.isSafeInteger(count) &&
      count > 0 &&
      typeof price === 'number' &&
      Number.isFinite(price) &&
      price > 0
    ) {
      recorded.push({ name, count, price, per })
    }
  }
  return recorded
}

interface ChargeLine {
  label: string
  basis?: {
    count: number
    unitPriceQuota: number
    per: number
    multipliers: number[]
  }
  quota: number
  tool?: boolean
  unit?: string
}

// Earlier token settlements only saved normalized billing_tokens for image-cache
// expressions. Text/cache quantities can be recovered from the request's log.
// Modality intersections and implicit long-write multipliers require the actual
// normalized snapshot; never infer them by solving backwards from the charge.
function historicalTextBillingTokens(
  log: Pick<UsageLog, 'prompt_tokens' | 'completion_tokens'>,
  other: LogOtherData
): Record<string, number> | undefined {
  const expression = compileBillingExpression(
    decodeBillingExprB64(other.expr_b64)
  )
  if (expression.status !== 'ready' || other.is_task || other.audio) return
  const vars = expression.variables
  if (
    (['img', 'img_cr', 'img_o', 'ai', 'ao'] as const).some((v) => vars.has(v))
  ) {
    return
  }
  const cache = getLogCacheUsage(log.prompt_tokens, other)
  const read = cache.read ?? 0
  const write = cache.write ?? 0
  const oneHour = other.cache_creation_tokens_1h ?? 0
  const twelveHours = other.cache_creation_tokens_12h ?? 0
  if (
    ![
      log.prompt_tokens,
      log.completion_tokens,
      read,
      write,
      oneHour,
      twelveHours,
    ].every((v) => Number.isSafeInteger(v) && v >= 0)
  ) {
    return
  }
  if (
    (oneHour && !vars.has('cc1h')) ||
    (twelveHours && !vars.has('cc12h')) ||
    oneHour + twelveHours > write
  ) {
    return
  }

  // Modern logs explicitly tag Anthropic usage. The older Claude rows with a
  // cache-write split also distinguished uncached input, in the log itself.
  const anthropic =
    other.usage_semantic === 'anthropic' ||
    (!other.usage_semantic &&
      other.claude === true &&
      (other.cache_creation_tokens_5m != null ||
        other.cache_creation_tokens_1h != null))
  if (
    (!other.usage_semantic && other.claude && !anthropic && (read || write)) ||
    (anthropic && twelveHours) ||
    (!anthropic && oneHour)
  ) {
    return
  }
  const shortWrites = write - oneHour - twelveHours
  let input = log.prompt_tokens
  if (anthropic) {
    if (!vars.has('cr')) input += read
    if (!vars.has('cc')) input += shortWrites
  } else {
    if (vars.has('cr')) input -= read
    if (vars.has('cc')) input -= shortWrites
    if (vars.has('cc12h')) input -= twelveHours
  }
  return {
    p: Math.max(0, input),
    c: log.completion_tokens,
    cr: read,
    cc: shortWrites,
    cc1h: oneHour,
    cc12h: twelveHours,
  }
}

// Only itemize prices and quantities recorded with this request. A partial or
// incompatible historical snapshot must never replace the settled total.
export function getChargeBreakdown(
  other: LogOtherData | null,
  settled: number,
  displayQuotaPerUnit: number,
  tools: BilledTool[],
  log?: Pick<UsageLog, 'prompt_tokens' | 'completion_tokens'> & Partial<Pick<UsageLog, 'model_name'>>
): ChargeLine[] {
  const quotaPerUnit = other?.billing_quota_per_unit ?? displayQuotaPerUnit
  const ratio = other?.group_ratio
  if (
    !other ||
    !Number.isFinite(quotaPerUnit) ||
    quotaPerUnit <= 0 ||
    typeof ratio !== 'number' ||
    !Number.isFinite(ratio) ||
    ratio < 0
  ) {
    return []
  }
  const line = (
    label: string,
    count: number,
    price: number,
    per: number,
    ruleRatios: number[] = []
  ): ChargeLine => ({
    label,
    basis: {
      count,
      unitPriceQuota: price * quotaPerUnit,
      per,
      multipliers: [ratio, ...ruleRatios],
    },
    unit: per === 1_000_000 ? 'tokens' : 'request',
    quota:
      ((count * price) / per) *
      quotaPerUnit *
      ratio *
      ruleRatios.reduce((product, factor) => product * factor, 1),
  })
  const toolLines = tools.map(({ name, count, price, per }) => ({
    ...line(name, count, price, per),
    tool: true,
    unit: 'times',
  }))
  const tokenLines: ChargeLine[] = []
  const summary = getTieredBillingSummary(other, log?.model_name)
  let quantitiesKnown = true
  if (
    summary &&
    !other.metered_usage_estimated &&
    (summary.priceEntries[0]?.unit ||
      summary.tiers.filter(
        (tier) =>
          normalizeTierLabel(tier.label) ===
          normalizeTierLabel(other.matched_tier)
      ).length === 1)
  ) {
    const separateRead = summary.priceEntries.some(
      (e) => e.field === 'cacheReadPrice'
    )
    const separateWrite = summary.priceEntries.some(
      (e) => e.field === 'cacheCreatePrice'
    )
    const counts =
      other.billing_tokens ?? (log && historicalTextBillingTokens(log, other))
    const rules = Array.isArray(other.request_rules)
      ? other.request_rules
          .filter((rule) => rule?.matched)
          .map((rule) => rule.multiplier)
      : []
    for (const entry of summary.priceEntries) {
      const variable = BILLING_PRICING_VARS.find((v) => v.field === entry.field)
      let count: number | undefined
      if (variable) {
        count = counts?.[variable.key]
      } else if (entry.unit === 'image') {
        count = other.image_count
      } else if (entry.unit === 'request') {
        count = 1
      } else if (entry.unit === other.metered_usage?.unit) {
        count = other.metered_usage?.count
      }
      if (typeof count !== 'number' || !Number.isFinite(count) || count < 0) {
        quantitiesKnown = false
      } else if (count > 0) {
        tokenLines.push({
          ...line(
            entry.field === 'inputPrice' &&
              separateRead &&
              (separateWrite ||
                !(other.cache_write_tokens || other.cache_creation_tokens))
              ? 'Uncached input'
              : entry.shortLabel,
            count,
            entry.price,
            variable ? 1_000_000 : 1,
            rules
          ),
          unit: variable ? 'tokens' : entry.unit,
        })
      }
    }
  } else if (
    !other.billing_mode &&
    typeof other.model_price === 'number' &&
    other.model_price >= 0 &&
    !other.is_task &&
    !other.audio_input_seperate_price
  ) {
    tokenLines.push(line('Per-call', 1, other.model_price, 1))
  } else if (
    !other.billing_mode &&
    log &&
    other.model_price === -1 &&
    typeof other.model_ratio === 'number' &&
    typeof other.completion_ratio === 'number' &&
    !other.audio &&
    !other.image &&
    !other.image_output &&
    !other.audio_input_seperate_price &&
    !other.cache_creation_tokens_1h &&
    !other.cache_creation_tokens_5m &&
    !other.cache_creation_tokens_12h
  ) {
    const read = other.cache_tokens ?? 0
    const write = other.cache_creation_tokens ?? 0
    const input =
      other.usage_semantic === 'anthropic' || other.claude
        ? log.prompt_tokens
        : Math.max(0, log.prompt_tokens - read - write)
    for (const [label, count, factor] of [
      ['Input', input, 1],
      ['Output', log.completion_tokens, other.completion_ratio],
      ['Cache Read', read, other.cache_ratio],
      ['Cache Write', write, other.cache_creation_ratio],
    ] as const) {
      if (count > 0 && typeof factor === 'number') {
        tokenLines.push(
          line(
            label,
            count,
            (other.model_ratio * factor * 1_000_000) / quotaPerUnit,
            1_000_000
          )
        )
      }
    }
  }
  // Keep input, its cache components, and output adjacent in the receipt.
  const all = [
    ...tokenLines.filter((l) => l.label !== 'Output'),
    ...tokenLines.filter((l) => l.label === 'Output'),
    ...toolLines,
  ]
  const total = all.reduce((sum, part) => sum + part.quota, 0)
  // The gateway rounds once after summing the fractional quota amounts.
  if (
    quantitiesKnown &&
    all.every((part) => Number.isFinite(part.quota) && part.quota >= 0) &&
    Math.round(total) === settled
  ) {
    return all
  }
  const toolQuota = toolLines.reduce((sum, part) => sum + part.quota, 0)
  if (toolLines.length && Number.isFinite(toolQuota) && toolQuota <= settled) {
    return [
      ...toolLines,
      ...(settled > toolQuota
        ? [
            {
              label: 'Remaining charge',
              quota: settled - toolQuota,
            },
          ]
        : []),
    ]
  }
  return []
}
