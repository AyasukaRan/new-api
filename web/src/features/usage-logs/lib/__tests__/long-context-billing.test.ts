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
import { describe, expect, it } from 'vitest'

import type { LogOtherData } from '../../types'
import { getLongContextBilling } from '../long-context-billing'

const gptExpression =
  'v1: len <= 272000 ? tier("standard", p * 2 + c * 10 + cr * 0.1) : tier("long_context", p * 4 + c * 15 + cr * 0.2)'

function settledLog(
  expression = gptExpression,
  inputTokens = 272001,
  tier = 'long_context'
): LogOtherData {
  return {
    billing_mode: 'tiered_expr',
    billing_unit: 'token',
    expr_b64: Buffer.from(expression).toString('base64'),
    matched_tier: tier,
    billing_tokens: { len: inputTokens, p: 1, cr: inputTokens - 1 },
  }
}

describe('long-context billing evidence', () => {
  it('marks the actually settled fallback tier using total context including cache', () => {
    expect(getLongContextBilling(settledLog())).toEqual({
      inputTokens: 272001,
      threshold: 272000,
      inclusive: false,
      tier: 'long_context',
    })
    expect(
      getLongContextBilling(settledLog(gptExpression, 272000, 'standard'))
    ).toBeNull()
  })

  it('recognizes an explicitly selected long tier without relying on its label', () => {
    const expression =
      'len >= 200000 ? tier("B", p * 6 + c * 22.5) : tier("A", p * 3 + c * 15)'
    expect(getLongContextBilling(settledLog(expression, 200000, 'B'))).toEqual({
      inputTokens: 200000,
      threshold: 200000,
      inclusive: true,
      tier: 'B',
    })
    expect(
      getLongContextBilling(settledLog(expression, 199999, 'A'))
    ).toBeNull()
  })

  it('keeps a proven context tier when a separate fast request multiplier also applies', () => {
    const expression = `(len <= 272000 ? tier("standard", p * 2 + c * 10) : tier("long_context", p * 4 + c * 15)) * (param("service_tier") == "fast" ? 2 : 1)`
    expect(getLongContextBilling(settledLog(expression))).toEqual({
      inputTokens: 272001,
      threshold: 272000,
      inclusive: false,
      tier: 'long_context',
    })
  })

  it('derives the selected lower bound through an ordered multi-threshold chain', () => {
    const expression =
      'len < 100000 ? tier("short", p * 1) : len <= 300000 ? tier("middle", p * 2) : tier("large", p * 4)'
    expect(
      getLongContextBilling(settledLog(expression, 100000, 'middle'))
    ).toEqual({
      inputTokens: 100000,
      threshold: 100000,
      inclusive: true,
      tier: 'middle',
    })
    expect(
      getLongContextBilling(settledLog(expression, 300001, 'large'))
    ).toEqual({
      inputTokens: 300001,
      threshold: 300000,
      inclusive: false,
      tier: 'large',
    })
  })

  it.each([
    ['missing actual settlement tokens', { billing_tokens: undefined }],
    ['estimated context omitted', { billing_tokens: { p: 300000 } }],
    ['non-finite context', { billing_tokens: { len: Infinity } }],
    ['negative context', { billing_tokens: { len: -1 } }],
    ['fractional context', { billing_tokens: { len: 272001.5 } }],
    ['fixed billing unit', { billing_unit: 'request' as const }],
    ['legacy ratio billing', { billing_mode: 'ratio' }],
    ['missing tier', { matched_tier: undefined }],
    ['mismatched settled tier', { matched_tier: 'standard' }],
    ['invalid expression', { expr_b64: 'invalid' }],
  ])('omits a tag for %s', (_, override) => {
    expect(getLongContextBilling({ ...settledLog(), ...override })).toBeNull()
  })

  it.each([
    [
      'same prices',
      'len <= 272000 ? tier("standard", p * 2) : tier("long_context", p * 2)',
    ],
    [
      'duplicate labels',
      'len <= 272000 ? tier("long_context", p * 2) : tier("long_context", p * 4)',
    ],
    [
      'fixed prices',
      'len <= 272000 ? tier("standard", fixed(1)) : tier("long_context", fixed(2))',
    ],
    [
      'output threshold',
      'c <= 272000 ? tier("standard", p * 2) : tier("long_context", p * 4)',
    ],
    [
      'uncached threshold',
      'p <= 272000 ? tier("standard", p * 2) : tier("long_context", p * 4)',
    ],
    [
      'service tier',
      'param("service_tier") == "fast" ? tier("long_context", p * 4) : tier("standard", p * 2)',
    ],
    [
      'time pricing',
      'hour() < 12 ? tier("standard", p * 2) : tier("long_context", p * 4)',
    ],
    ['flat price with misleading label', 'tier("long_context", p * 4)'],
    [
      'complex context predicate',
      'len <= 272000 && c < 100 ? tier("standard", p * 2) : tier("long_context", p * 4)',
    ],
    [
      'nested true branch',
      'len > 272000 ? (len > 400000 ? tier("huge", p * 8) : tier("long_context", p * 4)) : tier("standard", p * 2)',
    ],
    [
      'unreachable tier',
      'len <= 272000 ? tier("standard", p * 2) : len <= 100000 ? tier("unused", p * 3) : tier("long_context", p * 4)',
    ],
  ])('does not infer long-context pricing from %s', (_, expression) => {
    expect(getLongContextBilling(settledLog(expression))).toBeNull()
  })
})
