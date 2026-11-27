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
import { getCurrencyFractionDigits } from '@/lib/currency'

import type { ChannelBalanceSample, ChannelKeyUsageMonitoring } from '../types'
import { getChannelDisplayBalance } from './channel-utils'

/** Ledger costs are USD amounts, independent of the site's display currency. */
export function formatKeyUsageUSD(amount: number, locale?: string): string {
  const digits = getCurrencyFractionDigits(amount, {
    digitsLarge: 4,
    digitsSmall: 8,
    abbreviate: false,
  })
  const formatter = new Intl.NumberFormat(locale, {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 0,
    maximumFractionDigits: digits,
  })
  const minimum = 10 ** -digits
  return amount > 0 && amount < minimum
    ? `<${formatter.format(minimum)}`
    : formatter.format(amount)
}

export function getKeyBalanceHistory(
  history: ChannelBalanceSample[],
  keyIndex: number | null,
  multiplier?: number
): { timestamp: number; balance: number | null }[] {
  const points: { timestamp: number; balance: number | null }[] = []
  let previous: ChannelBalanceSample | undefined
  for (const sample of history) {
    const row = sample.key_balances.find((key) => key.index === keyIndex)
    // Without a segment marker old index-only observations cannot be joined safely.
    if (
      previous &&
      (sample.configuration_segment == null ||
        previous.configuration_segment == null ||
        previous.configuration_segment !== sample.configuration_segment)
    ) {
      points.push({
        timestamp: (previous.checked_at + sample.checked_at) / 2,
        balance: null,
      })
    }
    let balance: number | null = null
    if (keyIndex === null) {
      if (sample.success && !sample.partial) {
        balance = getChannelDisplayBalance(sample.balance, multiplier)
      }
    } else if (row && !row.error && Number.isFinite(row.balance)) {
      balance = getChannelDisplayBalance(row.balance, multiplier)
    }
    points.push({ timestamp: sample.checked_at, balance })
    previous = sample
  }
  return points
}

export function getKeyUsageTrend(
  data: ChannelKeyUsageMonitoring,
  identity: string
): { timestamp: number; tokens: number; cost: number | null }[] {
  const daily = data.window_end - data.window_start > 86400
  const buckets = new Map<
    number,
    { timestamp: number; tokens: number; cost: number | null }
  >()
  for (const point of data.series) {
    const key = point.keys.find((row) => row.key_identity === identity)
    if (!key) continue
    let timestamp = Math.floor(point.timestamp / 300) * 300
    if (daily) {
      const date = new Date(point.timestamp * 1000)
      date.setHours(0, 0, 0, 0)
      timestamp = date.getTime() / 1000
    }
    const bucket = buckets.get(timestamp) ?? {
      timestamp,
      tokens: 0,
      cost: null,
    }
    bucket.tokens += key.input_tokens + key.output_tokens
    if (key.cost_usd !== null && Number.isFinite(key.cost_usd)) {
      bucket.cost = (bucket.cost ?? 0) + key.cost_usd
    }
    buckets.set(timestamp, bucket)
  }
  return [...buckets.values()].sort((a, b) => a.timestamp - b.timestamp)
}

/** Account-level balance decline across consecutive valid observations only. */
export function getKeyDailyBalanceDecrease(
  history: ChannelBalanceSample[],
  keyIndex: number,
  multiplier?: number
): number | null {
  const segment = history.at(-1)?.configuration_segment
  if (segment === undefined) return null
  let previous: { timestamp: number; balance: number } | null = null
  let decrease = 0
  let seconds = 0
  for (const sample of history) {
    if (sample.configuration_segment !== segment) {
      previous = null
      continue
    }
    const row = sample.key_balances.find((key) => key.index === keyIndex)
    const balance =
      row && !row.error
        ? getChannelDisplayBalance(row.balance, multiplier)
        : null
    if (balance === null) {
      previous = null
      continue
    }
    if (previous && sample.checked_at > previous.timestamp) {
      decrease += Math.max(0, previous.balance - balance)
      seconds += sample.checked_at - previous.timestamp
    }
    previous = { timestamp: sample.checked_at, balance }
  }
  if (seconds < 86400 || !Number.isFinite(decrease)) return null
  return decrease / (seconds / 86400)
}
