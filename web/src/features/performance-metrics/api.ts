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
import { api } from '@/lib/api'

import type {
  AdminPerformanceMetricsData,
  PerformanceMetricsData,
  PerfSummaryAllData,
} from './types'

export const perfMetricsQueryKeys = {
  summaries: ['perf-metrics-summary'] as const,
  details: ['perf-metrics'] as const,
}

export async function getPerfMetricsSummary(
  hours = 24
): Promise<PerfSummaryAllData> {
  const res = await api.get<PerfSummaryAllData>('/api/perf-metrics/summary', {
    params: { hours },
  })
  return res.data
}

export async function getPerfMetrics(
  modelName: string,
  hours = 24
): Promise<PerformanceMetricsData> {
  const res = await api.get<PerformanceMetricsData>('/api/perf-metrics', {
    params: {
      model: modelName,
      hours,
    },
  })
  return res.data
}

export async function getAdminPerfMetrics(
  modelName: string,
  hours: number,
  signal: AbortSignal
): Promise<AdminPerformanceMetricsData> {
  const res = await api.get<AdminPerformanceMetricsData>(
    '/api/perf-metrics/admin',
    {
      params: { model: modelName, hours },
      signal,
      disableDuplicate: true,
    }
  )
  return res.data
}
