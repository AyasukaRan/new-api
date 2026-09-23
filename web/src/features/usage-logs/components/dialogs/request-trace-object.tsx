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
import { AlertTriangle, Download, FileBox } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useIsAdmin } from '@/hooks/use-admin'
import { useAuthStore } from '@/stores/auth-store'

import { getRequestTraceObject } from '../../api'
import type { RequestTraceLeg } from '../../types'

/** Media types the browser can render in place; everything else downloads. */
function previewKind(contentType: string): 'audio' | 'image' | 'video' | null {
  if (contentType.startsWith('audio/')) return 'audio'
  if (contentType.startsWith('image/')) return 'image'
  if (contentType.startsWith('video/')) return 'video'
  return null
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * Renders the binary payload of one trace leg — an uploaded recording, a
 * synthesized reply, a generated image — from object storage.
 *
 * The blob is fetched rather than linked so the request carries the console's
 * own credentials, and the object URL is revoked when the leg unmounts because
 * a dialog that is opened repeatedly would otherwise leak one per view.
 */
export function RequestTraceObject(props: {
  traceId: string
  leg: RequestTraceLeg
}) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const [objectUrl, setObjectUrl] = useState<string | null>(null)
  const contentType = props.leg.content_type ?? ''
  const kind = previewKind(contentType)

  const objectQuery = useQuery({
    queryKey: [
      'usage-logs',
      'request-trace-object',
      user?.id,
      user?.role,
      props.traceId,
      props.leg.seq,
    ],
    queryFn: ({ signal }) =>
      getRequestTraceObject(props.traceId, props.leg.seq, signal),
    enabled: isAdmin && user != null,
    retry: false,
    staleTime: 5 * 60 * 1000,
    gcTime: 0,
    placeholderData: undefined,
  })

  useEffect(() => {
    if (!objectQuery.data) return
    const url = URL.createObjectURL(objectQuery.data)
    setObjectUrl(url)
    return () => {
      URL.revokeObjectURL(url)
      setObjectUrl(null)
    }
  }, [objectQuery.data])

  const download = () => {
    if (!objectUrl) return
    const link = document.createElement('a')
    link.href = objectUrl
    link.download = `trace-${props.traceId}-${props.leg.seq}`
    link.click()
  }

  if (!isAdmin) return null

  return (
    <div className='bg-muted/30 space-y-2 rounded-md border p-3'>
      <div className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs'>
        <FileBox className='size-3.5' aria-hidden='true' />
        <span className='font-mono'>{contentType || t('Unknown type')}</span>
        {props.leg.body_size > 0 && (
          <span>{formatBytes(props.leg.body_size)}</span>
        )}
      </div>

      {objectQuery.isPending && (
        <Skeleton
          className='h-12 w-full rounded-md'
          aria-label={t('Loading...')}
        />
      )}

      {objectQuery.isError && (
        <p className='text-destructive flex items-center gap-1.5 text-xs'>
          <AlertTriangle className='size-3.5' aria-hidden='true' />
          {t('The stored payload could not be loaded. It may have expired.')}
        </p>
      )}

      {objectUrl && kind === 'audio' && (
        <audio controls src={objectUrl} className='w-full'>
          {t('Your browser cannot play this recording.')}
        </audio>
      )}
      {objectUrl && kind === 'image' && (
        <img
          src={objectUrl}
          alt={t('Captured payload')}
          className='max-h-72 max-w-full rounded-md object-contain'
        />
      )}
      {objectUrl && kind === 'video' && (
        <video controls src={objectUrl} className='max-h-72 w-full rounded-md'>
          {t('Your browser cannot play this recording.')}
        </video>
      )}
      {objectUrl && kind === null && (
        <p className='text-muted-foreground text-xs'>
          {t('This payload cannot be previewed in the browser.')}
        </p>
      )}

      {objectUrl && (
        <Button type='button' variant='outline' size='xs' onClick={download}>
          <Download className='size-3.5' aria-hidden='true' />
          {t('Download')}
        </Button>
      )}
    </div>
  )
}
