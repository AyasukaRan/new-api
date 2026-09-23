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
import { isAxiosError } from 'axios'

import { api } from '@/lib/api'
import {
  createServerError,
  requireServerSuccess,
} from '@/lib/server-error-message'

export type CaptureType =
  | 'cpu'
  | 'heap'
  | 'allocs'
  | 'goroutine'
  | 'mutex'
  | 'block'

export interface ProfilingStatus {
  pyroscope_configured: boolean
  pyroscope_running: boolean
  pyroscope_available: boolean
  app_name: string
  profile_types: { id: string; name: string; unit: string }[]
  pprof_enabled: boolean
  cpu_capture_available: boolean
  capture_types: CaptureType[]
  max_capture_seconds: number
}

export interface ProfileFrame {
  name: string
  depth: number
  start: number
  total: number
  self: number
}

export interface ProfileResult {
  source: 'pyroscope' | 'pprof'
  profile_type: string
  unit: string
  start: number
  end: number
  total: number
  flamegraph: ProfileFrame[]
  hotspots: { name: string; self: number; total: number }[]
  timeline: { timestamp: number; value: number }[]
  truncated: boolean
  download_id?: string
}

type Response<T> = { success: boolean; message?: string; data?: T }
const base = '/api/performance/profiling'

function profileResponse<T>(response: Response<T>): T {
  requireServerSuccess(response)
  if (response.data == null) {
    throw createServerError(response, 'Unable to load profiling data')
  }
  return response.data
}

export async function getProfilingStatus(
  signal?: AbortSignal
): Promise<ProfilingStatus> {
  const response = await api.get<Response<ProfilingStatus>>(`${base}/status`, {
    signal,
    disableDuplicate: true,
  })
  return profileResponse(response.data)
}

export async function queryProfile(
  params: { profile_type: string; start: number; end: number },
  signal?: AbortSignal
): Promise<ProfileResult> {
  const response = await api.post<Response<ProfileResult>>(
    `${base}/query`,
    params,
    { signal, timeout: 30000 }
  )
  return profileResponse(response.data)
}

export async function captureProfile(
  params: { profile_type: CaptureType; seconds: number },
  signal?: AbortSignal
): Promise<ProfileResult> {
  const response = await api.post<Response<ProfileResult>>(
    `${base}/capture`,
    params,
    { signal, timeout: 30000 }
  )
  return profileResponse(response.data)
}

async function profileDownloadError(body: Blob): Promise<Error> {
  if (body.size <= 65536 && body.type.toLowerCase().includes('json')) {
    try {
      const payload: unknown = JSON.parse(await body.text())
      if (
        payload &&
        typeof payload === 'object' &&
        'message' in payload &&
        typeof payload.message === 'string'
      ) {
        return createServerError(
          { message: payload.message },
          'Unable to download profile'
        )
      }
    } catch {
      // Do not display a proxy's raw error body or malformed JSON.
    }
  }
  return createServerError({ message: 'Unable to download profile' })
}

export async function downloadProfile(
  id: string,
  signal?: AbortSignal
): Promise<Blob> {
  let body: Blob
  try {
    const response = await api.get<Blob>(
      `${base}/profiles/${encodeURIComponent(id)}`,
      { responseType: 'blob', signal, disableDuplicate: true }
    )
    body = response.data
  } catch (error) {
    if (isAxiosError(error) && error.response?.data instanceof Blob) {
      throw await profileDownloadError(error.response.data)
    }
    throw error
  }
  if (body.type.toLowerCase().includes('json')) {
    throw await profileDownloadError(body)
  }
  return body
}
