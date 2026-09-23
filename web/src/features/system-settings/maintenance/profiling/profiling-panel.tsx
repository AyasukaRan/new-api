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
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getProfilingStatus } from './api'
import { getProfilingErrorMessage } from './error-message'
import { ProfilingWorkspace } from './profiling-workspace'

export function ProfilingPanel() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const role = useAuthStore((state) => state.auth.user?.role ?? 0)
  if (!userId || role < ROLE.SUPER_ADMIN) return null
  return <ProfilingStatusPanel key={`${userId}:${role}`} userId={userId} />
}

function ProfilingStatusPanel(props: { userId: number }) {
  const { t } = useTranslation()
  const status = useQuery({
    queryKey: ['performance', 'profiling', 'status', props.userId],
    queryFn: ({ signal }) => getProfilingStatus(signal),
    staleTime: 30000,
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  return (
    <section
      aria-label={t('Performance profiling')}
      className='min-w-0 space-y-4'
    >
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='space-y-1'>
          <h4 className='font-medium'>{t('Performance profiling')}</h4>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Inspect CPU, memory, goroutines, and contention without leaving the console.'
            )}
          </p>
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={status.isFetching}
          onClick={() => void status.refetch()}
        >
          {t('Refresh collection status')}
        </Button>
      </div>
      {status.isPending && <LoadingState className='min-h-32' />}
      {status.isError && (
        <ErrorState
          className='min-h-40'
          title={t('Unable to load profiling data')}
          description={getProfilingErrorMessage(
            status.error,
            t,
            t('Unable to load profiling data')
          )}
          onRetry={() => void status.refetch()}
        />
      )}
      {status.isSuccess && (
        <>
          <div className='flex flex-wrap items-center gap-2'>
            <StatusBadge
              copyable={false}
              variant={status.data.pyroscope_running ? 'success' : 'neutral'}
            >
              {status.data.pyroscope_running
                ? t('Continuous collection running')
                : t('Continuous collection stopped')}
            </StatusBadge>
            <StatusBadge
              copyable={false}
              variant={status.data.pyroscope_available ? 'success' : 'warning'}
            >
              {status.data.pyroscope_available
                ? t('Profile storage connected')
                : t('Profile storage unavailable')}
            </StatusBadge>
            <StatusBadge
              copyable={false}
              variant={status.data.pprof_enabled ? 'info' : 'neutral'}
            >
              {status.data.pprof_enabled
                ? t('Instant capture enabled')
                : t('Instant capture disabled')}
            </StatusBadge>
            {status.data.app_name && (
              <span
                className='text-muted-foreground truncate text-xs'
                title={status.data.app_name}
              >
                {status.data.app_name}
              </span>
            )}
          </div>
          <ProfilingWorkspace
            userId={props.userId}
            status={status.data}
            refreshStatus={() => void status.refetch()}
          />
        </>
      )}
    </section>
  )
}
