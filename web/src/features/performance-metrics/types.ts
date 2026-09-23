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
export type PerformanceSummary = {
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
}

export type PerformanceSeriesPoint = {
  ts: number
  avg_ttft_ms: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
}

export type PerformanceGroup = {
  group: string
  avg_ttft_ms: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  series: PerformanceSeriesPoint[]
  availability_rate?: number | null
  availability_series?: SuccessRatePoint[] | null
}

export type PerformanceChannel = {
  channel_index: number
  current_available?: boolean
  current_observed_at?: number
  success_rate?: number | null
  availability_rate?: number | null
  avg_latency_ms: number
  avg_tps: number
  series?: SuccessRatePoint[] | null
}

export type PerformanceMetricsData = {
  success: boolean
  message?: string
  data: {
    summary?: PerformanceSummary | null
    series?: PerformanceSeriesPoint[]
    window_start?: number
    window_end?: number
    model_name: string
    avg_latency_ms?: number
    avg_tps?: number
    current_available?: boolean
    current_observed_at?: number
    series_schema?: string
    groups: PerformanceGroup[]
    availability_rate?: number | null
    availability_series?: SuccessRatePoint[] | null
    channels?: PerformanceChannel[] | null
  }
}

export type AdminPerformanceKey = {
  key_index: number
  key_hint: string
  enabled: boolean
  current_available?: boolean | null
  observed_at: number
  success?: boolean
  source?: 'request' | 'probe'
  status_code?: number
  error?: string
}

export type AdminPerformanceChannel = {
  channel_id: number
  channel_name: string
  status: number
  current_available?: boolean | null
  current_observed_at?: number
  availability_rate?: number | null
  keys: AdminPerformanceKey[]
}

export type AdminPerformanceMetricsData = {
  success: boolean
  message?: string
  data: {
    model_name: string
    channels: AdminPerformanceChannel[]
  }
}

export type SuccessRatePoint = { ts: number; success_rate: number }

export type PerfModelSummary = {
  model_name: string
  current_available?: boolean
  current_observed_at?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_series?: SuccessRatePoint[]
  request_count?: number
  availability_rate?: number | null
  availability_series?: SuccessRatePoint[] | null
}

export type PerfSummaryAllData = {
  success: boolean
  message?: string
  data: {
    summary?: PerformanceSummary | null
    window_start?: number
    window_end?: number
    models: PerfModelSummary[]
  }
}
