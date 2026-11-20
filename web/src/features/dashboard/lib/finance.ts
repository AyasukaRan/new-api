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
import type { DashboardMetric, QuotaDataItem } from '../types'

export function getDashboardMetricValue(
  item: Pick<QuotaDataItem, 'quota' | 'count' | 'finance'>,
  metric: DashboardMetric
): number | null {
  if (metric === 'requests') return Number(item.count) || 0
  if (metric === 'quota') {
    const quota = Number(item.quota) || 0
    const free = Number(item.finance?.free_quota) || 0
    return Math.max(0, quota - Math.max(0, free))
  }
  if (!item.finance || item.finance.recorded_count <= 0) return null
  const value =
    metric === 'cost' ? item.finance.cost_quota : item.finance.profit_quota
  return Number.isFinite(value) ? value : null
}
