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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import { downloadProfile, type ProfileResult } from './api'
import { getProfilingErrorMessage } from './error-message'
import { ProfileFlamegraph } from './profile-flamegraph'
import { formatProfileValue } from './profile-value'

export function ProfileResults(props: { profile: ProfileResult }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const download = useMutation({
    mutationFn: async () => {
      controller.current?.abort()
      controller.current = new AbortController()
      let blob: Blob
      let extension = 'json'
      if (props.profile.download_id) {
        blob = await downloadProfile(
          props.profile.download_id,
          controller.current.signal
        )
        extension = 'pprof'
      } else {
        blob = new Blob([JSON.stringify(props.profile)], {
          type: 'application/json',
        })
      }
      if (controller.current.signal.aborted) return
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `profile-${props.profile.start}.${extension}`
      anchor.click()
      URL.revokeObjectURL(url)
    },
    retry: false,
    meta: { errorToast: false },
  })
  const hasSamples =
    props.profile.total > 0 &&
    (props.profile.flamegraph.length > 0 || props.profile.hotspots.length > 0)

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='space-y-1'>
          <p className='text-sm font-medium'>
            {t('Profile total')}:{' '}
            {formatProfileValue(
              props.profile.total,
              props.profile.unit,
              locale
            )}
          </p>
          <p className='text-muted-foreground text-xs'>
            {formatTimestampToDate(props.profile.start, 'milliseconds')} —{' '}
            {formatTimestampToDate(props.profile.end, 'milliseconds')}
          </p>
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={download.isPending}
          onClick={() => download.mutate()}
        >
          {props.profile.download_id
            ? t('Download pprof')
            : t('Download profile JSON')}
        </Button>
      </div>
      {props.profile.download_id && (
        <p className='text-muted-foreground text-xs'>
          {t('The pprof download is available for 5 minutes after capture.')}
        </p>
      )}
      {download.isError && (
        <Alert variant='destructive'>
          <AlertDescription>
            {getProfilingErrorMessage(
              download.error,
              t,
              t('Unable to download profile')
            )}
          </AlertDescription>
        </Alert>
      )}
      {props.profile.truncated && (
        <Alert>
          <AlertDescription>
            {t(
              'This profile exceeds the display limit. Only part of the call stack and the top 100 functions are shown.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {!hasSamples ? (
        <EmptyState
          className='min-h-40'
          title={t('No profile samples')}
          description={t(
            'Try another time range or collect a new profile while the service is handling requests.'
          )}
        />
      ) : (
        <Tabs
          defaultValue={
            props.profile.flamegraph.length > 0 ? 'flamegraph' : 'hotspots'
          }
        >
          <TabsList aria-label={t('Profile visualization')}>
            <TabsTrigger
              value='flamegraph'
              disabled={props.profile.flamegraph.length === 0}
            >
              {t('Flame graph')}
            </TabsTrigger>
            <TabsTrigger value='hotspots'>{t('Function hotspots')}</TabsTrigger>
          </TabsList>
          <TabsContent value='flamegraph' className='mt-3'>
            <ProfileFlamegraph profile={props.profile} />
          </TabsContent>
          <TabsContent value='hotspots' className='mt-3 space-y-3'>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Self excludes child calls; total includes them. Functions are ranked by self value.'
              )}
            </p>
            <StaticDataTable
              className='max-h-96 overflow-auto'
              tableClassName='min-w-[600px]'
              tableProps={{
                'aria-label': t('Function hotspots'),
                withContainer: false,
              }}
              data={props.profile.hotspots.slice(0, 100)}
              getRowKey={(row) => row.name}
              columns={[
                {
                  id: 'name',
                  header: t('Function'),
                  className: 'w-1/2',
                  cellClassName: 'max-w-xs font-mono text-xs',
                  cell: (row) => row.name,
                },
                {
                  id: 'self',
                  header: t('Self'),
                  cell: (row) =>
                    formatProfileValue(row.self, props.profile.unit, locale),
                },
                {
                  id: 'total',
                  header: t('Total'),
                  cell: (row) =>
                    formatProfileValue(row.total, props.profile.unit, locale),
                },
                {
                  id: 'share',
                  header: t('Self share'),
                  cell: (row) =>
                    `${formatNumber((row.self / props.profile.total) * 100, locale)}%`,
                },
              ]}
              emptyContent={t('No profile samples')}
            />
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}
