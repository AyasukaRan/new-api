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
import { ArrowUpRight, Route } from 'lucide-react'
import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { useIsAdmin } from '@/hooks/use-admin'
import { useAuthStore } from '@/stores/auth-store'

import type { LogOtherData } from '../../types'

const RequestTraceViewer = lazy(() => import('./request-trace-viewer'))

export function RequestTraceSection(props: {
  traceId: string
  enabled: boolean
  metadata?: LogOtherData | null
}) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const [open, setOpen] = useState(false)

  if (!props.enabled || !isAdmin || !props.traceId.trim()) return null

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title={t('Request Trace')}
      description={t('Inspect the conversation and each request exchange.')}
      trigger={
        <Button
          type='button'
          variant='outline'
          className='h-auto w-full justify-between gap-3 py-3'
        >
          <span className='flex min-w-0 items-center gap-2'>
            <Route className='size-4 shrink-0' aria-hidden='true' />
            {t('View request trace')}
          </span>
          <ArrowUpRight className='size-4 shrink-0' aria-hidden='true' />
        </Button>
      }
      contentClassName='w-[calc(100vw-1.5rem)] max-w-none gap-3 sm:max-w-[min(96vw,1440px)]'
      headerClassName='pr-8'
      descriptionClassName='sr-only'
      contentHeight='min(78dvh, 900px)'
      bodyClassName='h-full min-h-0 overflow-hidden py-0'
    >
      {open && (
        <Suspense fallback={<LoadingState />}>
          <RequestTraceViewer
            key={`${user?.id}:${user?.role}:${props.traceId}`}
            traceId={props.traceId}
            metadata={props.metadata}
          />
        </Suspense>
      )}
    </Dialog>
  )
}
