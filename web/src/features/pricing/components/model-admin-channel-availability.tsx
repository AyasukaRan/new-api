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

import { CopyButton } from '@/components/copy-button'
import {
  StaticDataTable,
  staticDataTableClassNames as tableStyles,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from '@/components/ui/accordion'
import { getAdminPerfMetrics } from '@/features/performance-metrics/api'
import { formatUptimePct } from '@/features/performance-metrics/lib/format'
import type { AdminPerformanceChannel } from '@/features/performance-metrics/types'
import { useIsAdmin } from '@/hooks/use-admin'
import { formatDateTimeStr } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { ModelCurrentAvailability } from './model-perf-badge'

export function ModelAdminChannelAvailability(props: { modelName: string }) {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const metricsQuery = useQuery({
    queryKey: ['perf-metrics-admin', user?.id, user?.role, props.modelName, 24],
    queryFn: ({ signal }) => getAdminPerfMetrics(props.modelName, 24, signal),
    enabled: isAdmin && user != null,
    staleTime: 60 * 1000,
    refetchInterval: 60 * 1000,
    gcTime: 0,
    placeholderData: undefined,
  })

  if (!isAdmin) return null
  if (metricsQuery.isLoading) {
    return <LoadingState size='sm' className='min-h-24' />
  }
  if (metricsQuery.isError || metricsQuery.data?.success === false) {
    return (
      <ErrorState
        className='min-h-24'
        onRetry={() => void metricsQuery.refetch()}
      />
    )
  }
  const channels = metricsQuery.data?.data.channels ?? []
  if (channels.length === 0) {
    return <EmptyState title={t('Not monitored')} bordered />
  }

  return (
    <Accordion multiple className='rounded-lg border px-3'>
      {channels.map((channel) => (
        <AccordionItem key={channel.channel_id} value={channel.channel_id}>
          <AccordionTrigger className='items-center gap-3 hover:no-underline'>
            <span className='flex min-w-0 flex-1 flex-wrap items-center justify-between gap-x-4 gap-y-2'>
              <span className='flex min-w-0 flex-wrap items-center gap-2'>
                <span className='break-all'>
                  {channel.channel_name || t('Channel')}
                </span>
                <span className='text-muted-foreground font-mono text-xs'>
                  #{channel.channel_id}
                </span>
                {channel.status !== 1 && (
                  <StatusBadge
                    label={t('Disabled')}
                    variant='neutral'
                    copyable={false}
                    type='text'
                  />
                )}
              </span>
              <span className='flex flex-wrap items-center gap-x-4 gap-y-2'>
                <ModelCurrentAvailability
                  available={channel.current_available}
                  observedAt={channel.current_observed_at}
                />
                <span className='text-muted-foreground text-xs'>
                  {t('Availability (last 24h)')}
                  <span className='text-foreground ml-2 font-mono'>
                    {channel.availability_rate != null &&
                    Number.isFinite(channel.availability_rate)
                      ? formatUptimePct(channel.availability_rate)
                      : t('Not monitored')}
                  </span>
                </span>
              </span>
            </span>
          </AccordionTrigger>
          <AccordionContent>
            <ModelChannelKeys channel={channel} />
          </AccordionContent>
        </AccordionItem>
      ))}
    </Accordion>
  )
}

function ModelChannelKeys(props: { channel: AdminPerformanceChannel }) {
  const { t } = useTranslation()
  return (
    <StaticDataTable
      className='rounded-lg'
      tableClassName='text-sm'
      tableProps={{
        'aria-label': t('Key availability for {{channel}}', {
          channel: props.channel.channel_name,
        }),
      }}
      headerRowClassName={tableStyles.compactHeaderRow}
      data={props.channel.keys ?? []}
      getRowKey={(key) => key.key_index}
      emptyContent={t('No keys found')}
      columns={[
        {
          id: 'index',
          header: '#',
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) => (
            <span className='font-mono'>#{key.key_index + 1}</span>
          ),
        },
        {
          id: 'key',
          header: t('Masked key'),
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) => (
            <div className='flex flex-col items-start gap-1'>
              <span className='font-mono'>{key.key_hint || '—'}</span>
              {!key.enabled && (
                <StatusBadge
                  label={t('Disabled')}
                  variant='neutral'
                  type='text'
                  copyable={false}
                />
              )}
            </div>
          ),
        },
        {
          id: 'current',
          header: t('Current status'),
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) => (
            <ModelCurrentAvailability
              available={key.current_available}
              observedAt={key.observed_at}
            />
          ),
        },
        {
          id: 'observed',
          header: t('Last observed'),
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) =>
            key.observed_at > 0 && Number.isFinite(key.observed_at)
              ? formatDateTimeStr(new Date(key.observed_at * 1000))
              : '—',
        },
        {
          id: 'status-code',
          header: t('Status Code'),
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) =>
            key.status_code != null && key.status_code > 0
              ? key.status_code
              : '—',
        },
        {
          id: 'error',
          header: t('Latest error'),
          className: tableStyles.compactHeaderCell,
          cellClassName: tableStyles.compactTopCell,
          cell: (key) => {
            const error = key.error?.trim()
            if (error) {
              return (
                <div className='flex min-w-48 items-start gap-2'>
                  <span className='text-destructive max-w-md wrap-anywhere whitespace-pre-wrap'>
                    {error}
                  </span>
                  <CopyButton
                    value={error}
                    size='icon'
                    className='size-6'
                    aria-label={t('Copy error details')}
                  />
                </div>
              )
            }
            if (key.success === false) return t('No error details available')
            return '—'
          },
        },
      ]}
    />
  )
}
