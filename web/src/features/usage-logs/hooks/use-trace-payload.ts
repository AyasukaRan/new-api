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

import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'

import { useAuthStore } from '@/stores/auth-store'

import type { TracePayloadView } from '../lib/request-trace-payload'
import {
  readTracePayload,
  type TracePayloadProgress,
} from '../lib/request-trace-stream'
import type { RequestTraceLeg } from '../types'

export function useTracePayload(
  traceId: string,
  leg: RequestTraceLeg | undefined,
  view: TracePayloadView,
  enabled = true
) {
  const user = useAuthStore((state) => state.auth.user)
  const identity = `${user?.id}:${user?.role}:${traceId}:${leg?.seq}:${view}`
  const [partial, setPartial] = useState<{
    identity: string
    progress: TracePayloadProgress
  }>()
  const query = useQuery({
    queryKey: [
      'usage-logs',
      'request-trace-payload',
      user?.id,
      user?.role,
      traceId,
      leg?.seq,
      view,
    ],
    queryFn: async ({ signal }) => {
      if (!leg) throw new Error('Failed to load the request trace')
      setPartial({ identity, progress: { bytes: 0 } })
      const onProgress = (progress: TracePayloadProgress) => {
        if (!signal.aborted) setPartial({ identity, progress })
      }
      const payload = await readTracePayload(
        traceId,
        leg,
        view,
        signal,
        onProgress
      )
      // Unknown response formats still have inspectable text/JSON in the raw capture.
      if (view === 'rendered' && !payload.leg.rendered) {
        return readTracePayload(
          traceId,
          leg,
          'conversation',
          signal,
          onProgress
        )
      }
      return payload
    },
    enabled:
      enabled && Boolean(leg?.has_body) && Boolean(user && user.role >= 10),
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    meta: { errorToast: false },
  })
  useEffect(() => {
    if (query.data) setPartial(undefined)
  }, [query.data])
  return {
    ...query,
    progress: partial?.identity === identity ? partial.progress : undefined,
  }
}
