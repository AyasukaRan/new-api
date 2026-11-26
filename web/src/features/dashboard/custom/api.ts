import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  DashboardConfig,
  DashboardConfigResponse,
  LiveDashboardResponse,
} from './types'

interface Envelope<T> {
  success: boolean
  message?: string
  data: T
}

export async function getDashboardConfig(
  signal?: AbortSignal
): Promise<DashboardConfigResponse> {
  const response = await api.get<Envelope<DashboardConfigResponse>>(
    '/api/user/self/dashboards',
    // React Query owns cancellation and deduplication for each observer set.
    { signal, disableDuplicate: true }
  )
  return requireServerSuccess(response.data).data
}

export async function saveDashboardConfig(
  revision: number,
  config: DashboardConfig
): Promise<DashboardConfigResponse> {
  const response = await api.put<Envelope<DashboardConfigResponse>>(
    '/api/user/self/dashboards',
    { revision, config }
  )
  return requireServerSuccess(response.data).data
}

export async function getLiveDashboard(
  params: { seconds: number; step: number; model?: string },
  signal?: AbortSignal
): Promise<LiveDashboardResponse> {
  const response = await api.get<Envelope<LiveDashboardResponse>>(
    '/api/dashboard/live',
    { params, signal, disableDuplicate: true }
  )
  return requireServerSuccess(response.data).data
}
