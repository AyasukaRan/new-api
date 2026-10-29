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
import type { DashboardFinance, DashboardMetric, QuotaDataItem } from '../types'

export function getDashboardMetricValue(
  item: QuotaDataItem,
  metric: DashboardMetric
): number | null {
  if (metric === 'requests') return Number(item.count) || 0
  if (metric === 'quota') return Number(item.quota) || 0
  if (!item.finance || item.finance.recorded_count <= 0) return null
  const value =
    metric === 'cost' ? item.finance.cost_quota : item.finance.profit_quota
  return Number.isFinite(value) ? value : null
}

export function summarizeDashboardFinance(
  data: Array<Pick<QuotaDataItem, 'quota' | 'count' | 'finance'>>
) {
  const summary: DashboardFinance & { revenue_quota: number; count: number } = {
    revenue_quota: 0,
    count: 0,
    cost_quota: 0,
    profit_quota: 0,
    covered_revenue_quota: 0,
    recorded_count: 0,
    actual_count: 0,
    estimated_count: 0,
    unrecorded_count: 0,
  }
  for (const row of data) {
    summary.revenue_quota += Number(row.quota) || 0
    summary.count += Number(row.count) || 0
    if (!row.finance) {
      summary.unrecorded_count += Number(row.count) || 0
      continue
    }
    for (const key of [
      'cost_quota',
      'profit_quota',
      'covered_revenue_quota',
      'recorded_count',
      'actual_count',
      'estimated_count',
      'unrecorded_count',
    ] as const) {
      const value = row.finance[key]
      if (Number.isFinite(value)) summary[key] += value
    }
  }
  return summary
}
