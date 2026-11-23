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
import { BILLING_PRICING_VARS } from '@/features/pricing/lib/billing-expr'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'
import { getTieredBillingSummary } from './format'

// A single quota unit is often split across several token or tool lines.
export function formatReceiptQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 9,
    digitsSmall: 9,
    abbreviate: false,
  })
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
  formula: string
  quota: number
  tool?: boolean
}

// Only itemize prices and quantities recorded with this request. A partial or
// incompatible historical snapshot must never replace the settled total.
export function getChargeBreakdown(
  other: LogOtherData | null,
  settled: number,
  quotaPerUnit: number,
  tools: BilledTool[],
  log?: Pick<UsageLog, 'prompt_tokens' | 'completion_tokens'>
): ChargeLine[] {
  const ratio = other?.group_ratio
  if (
    !other ||
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
    formula: [
      `${formatNumber(count)} × ${formatReceiptQuota(price * quotaPerUnit)} / ${formatNumber(per)}`,
      ratio,
      ...ruleRatios,
    ].join(' × '),
    quota:
      ((count * price) / per) *
      quotaPerUnit *
      ratio *
      ruleRatios.reduce((product, factor) => product * factor, 1),
  })
  const toolLines = tools.map(({ name, count, price, per }) => ({
    ...line(name, count, price, per),
    tool: true,
  }))
  const tokenLines: ChargeLine[] = []
  const summary = getTieredBillingSummary(other)
  if (summary) {
    const rules = Array.isArray(other.request_rules)
      ? other.request_rules
          .filter((rule) => rule?.matched)
          .map((rule) => rule.multiplier)
      : []
    for (const entry of summary.priceEntries) {
      const variable = BILLING_PRICING_VARS.find((v) => v.field === entry.field)
      let count: number | undefined
      if (variable) {
        count = other.billing_tokens?.[variable.key]
      } else if (entry.unit === 'image') {
        count = other.image_count
      } else if (entry.unit === 'request') {
        count = 1
      } else if (entry.unit === other.metered_usage?.unit) {
        count = other.metered_usage?.count
      }
      if (typeof count === 'number' && Number.isFinite(count) && count > 0) {
        tokenLines.push(
          line(
            entry.shortLabel,
            count,
            entry.price,
            variable ? 1_000_000 : 1,
            rules
          )
        )
      }
    }
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
  const all = [...tokenLines, ...toolLines]
  const total = all.reduce((sum, part) => sum + part.quota, 0)
  // The gateway rounds once after summing the fractional quota amounts.
  if (
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
              formula: '',
              quota: settled - toolQuota,
            },
          ]
        : []),
    ]
  }
  return []
}
