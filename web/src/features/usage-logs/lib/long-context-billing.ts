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
import { splitBillingExprAndRequestRules } from '@/features/pricing/lib/billing-expr'
import { readTokenTierChain } from '@/features/pricing/lib/billing-expression/display'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { TOKEN_VARIABLES } from '@/features/pricing/lib/billing-expression/types'

import type { LogOtherData } from '../types'
import { decodeBillingExprB64 } from './format'

export interface LongContextBilling {
  inputTokens: number
  threshold: number
  inclusive: boolean
  tier: string
}

/** Only settled token snapshots and canonical context-only tier chains are evidence. */
export function getLongContextBilling(
  other: LogOtherData | null | undefined
): LongContextBilling | null {
  const inputTokens = other?.billing_tokens?.len
  if (
    other?.billing_mode !== 'tiered_expr' ||
    other.billing_unit !== 'token' ||
    typeof inputTokens !== 'number' ||
    !Number.isSafeInteger(inputTokens) ||
    inputTokens < 0 ||
    !other.matched_tier
  ) {
    return null
  }

  const expression = decodeBillingExprB64(other.expr_b64)
  if (compileBillingExpression(expression).status !== 'ready') return null
  const { billingExpr } = splitBillingExprAndRequestRules(expression)
  const compiled = compileBillingExpression(billingExpr)
  if (
    compiled.status !== 'ready' ||
    compiled.ast.kind !== 'conditional' ||
    [...compiled.functions].some((name) => name !== 'tier')
  ) {
    return null
  }
  const tiers = readTokenTierChain(compiled.ast)
  if (
    !tiers ||
    tiers.length < 2 ||
    new Set(tiers.map((tier) => tier.label)).size !== tiers.length
  ) {
    return null
  }

  // The final else has no condition of its own; retain the preceding false bounds.
  let remaining = {
    min: 0,
    max: Number.MAX_SAFE_INTEGER,
    threshold: 0,
    inclusive: true,
  }
  const ranges: Array<typeof remaining> = []
  for (const tier of tiers.slice(0, -1)) {
    const condition = tier.conditions[0]
    if (
      tier.conditions.length !== 1 ||
      condition.var !== 'len' ||
      !Number.isSafeInteger(condition.value) ||
      condition.value <= 0 ||
      condition.value >= Number.MAX_SAFE_INTEGER
    ) {
      return null
    }

    const yes = { ...remaining }
    const no = { ...remaining }
    const upperCondition = condition.op === '<' || condition.op === '<='
    const lower = upperCondition ? no : yes
    const upper = upperCondition ? yes : no
    const inclusive = condition.op === '<' || condition.op === '>='
    const lowerToken = condition.value + (inclusive ? 0 : 1)
    if (lowerToken > lower.min) {
      lower.min = lowerToken
      lower.threshold = condition.value
      lower.inclusive = inclusive
    }
    upper.max = Math.min(upper.max, lowerToken - 1)
    if (yes.min > yes.max || no.min > no.max) return null
    ranges.push(yes)
    remaining = no
  }
  ranges.push(remaining)

  const index = ranges.findIndex(
    (range) => range.min <= inputTokens && inputTokens <= range.max
  )
  if (index < 0 || tiers[index].label !== other.matched_tier) return null
  const selected = ranges[index]
  if (selected.min === 0) return null
  const previous = ranges.findIndex((range) => range.max === selected.min - 1)
  if (
    previous < 0 ||
    !TOKEN_VARIABLES.some(
      (variable) =>
        (tiers[index].prices[variable] ?? 0) !==
        (tiers[previous].prices[variable] ?? 0)
    )
  ) {
    return null
  }
  return {
    inputTokens,
    threshold: selected.threshold,
    inclusive: selected.inclusive,
    tier: other.matched_tier,
  }
}
