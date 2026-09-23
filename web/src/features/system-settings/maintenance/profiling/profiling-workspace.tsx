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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  captureProfile,
  queryProfile,
  type CaptureType,
  type ProfilingStatus,
} from './api'
import { getProfilingErrorMessage } from './error-message'
import { ProfileResults } from './profile-results'

type WorkspaceProps = {
  userId: number
  status: ProfilingStatus
  refreshStatus: () => void
}

export function ProfilingWorkspace(props: WorkspaceProps) {
  const { t } = useTranslation()
  return (
    <Tabs defaultValue='continuous'>
      <TabsList aria-label={t('Profiling source')} className='h-auto flex-wrap'>
        <TabsTrigger value='continuous'>
          {t('Continuous profiles')} · Pyroscope
        </TabsTrigger>
        <TabsTrigger value='capture'>
          {t('Instant capture')} · pprof
        </TabsTrigger>
      </TabsList>
      <TabsContent value='continuous' className='mt-4'>
        <ContinuousProfiles {...props} />
      </TabsContent>
      <TabsContent value='capture' className='mt-4'>
        <InstantCapture
          key={`${props.status.pprof_enabled}:${props.status.cpu_capture_available}:${props.status.capture_types.join()}`}
          status={props.status}
          refreshStatus={props.refreshStatus}
        />
      </TabsContent>
    </Tabs>
  )
}

function ContinuousProfiles(props: WorkspaceProps) {
  const { t } = useTranslation()
  const id = useId()
  const [selectedType, setSelectedType] = useState('')
  const [window, setWindow] = useState(() => ({ minutes: 60, end: Date.now() }))
  const type =
    props.status.profile_types.find((profile) => profile.id === selectedType)
      ?.id ??
    props.status.profile_types.find((profile) => profile.name === 'CPU')?.id ??
    props.status.profile_types[0]?.id ??
    ''
  const start = window.end - window.minutes * 60 * 1000
  const profile = useQuery({
    queryKey: [
      'performance',
      'profiling',
      'query',
      props.userId,
      props.status.app_name,
      type,
      start,
      window.end,
    ],
    queryFn: ({ signal }) =>
      queryProfile({ profile_type: type, start, end: window.end }, signal),
    enabled:
      props.status.pyroscope_configured &&
      props.status.pyroscope_available &&
      Boolean(type),
    gcTime: 0,
    staleTime: 30000,
    retry: false,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const names: Record<string, string> = {
    CPU: t('CPU'),
    'Allocated objects': t('Allocated objects'),
    'Allocated memory': t('Allocated memory'),
    'Live objects': t('Live objects'),
    'Live memory': t('Live memory'),
    Goroutines: t('Goroutines'),
    'Mutex contention': t('Mutex contention'),
    'Mutex wait': t('Mutex wait'),
    'Blocking events': t('Blocking events'),
    'Blocking time': t('Blocking time'),
  }
  if (!props.status.pyroscope_configured) {
    return (
      <EmptyState
        className='min-h-40'
        title={t('Continuous profiling is not configured')}
        description={t(
          'Configure Pyroscope on the server to browse continuous profiles here. Instant capture can be used independently.'
        )}
      />
    )
  }
  if (!props.status.pyroscope_available) {
    return (
      <ErrorState
        className='min-h-40'
        title={t('Profile storage unavailable')}
        description={t(
          'Check the profiling service connection, then refresh collection status.'
        )}
        onRetry={props.refreshStatus}
      />
    )
  }
  if (!type) {
    return (
      <EmptyState
        className='min-h-40'
        title={t('No profile types available')}
        description={t(
          'The collector has not published profiles for this application yet.'
        )}
      />
    )
  }
  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-end gap-3'>
        <div className='grid gap-1.5'>
          <Label htmlFor={`${id}-type`}>{t('Profile type')}</Label>
          <NativeSelect
            id={`${id}-type`}
            value={type}
            onChange={(event) => setSelectedType(event.target.value)}
          >
            {props.status.profile_types.map((item) => (
              <NativeSelectOption key={item.id} value={item.id}>
                {names[item.name] ?? item.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor={`${id}-range`}>{t('Time range')}</Label>
          <NativeSelect
            id={`${id}-range`}
            value={window.minutes}
            onChange={(event) =>
              setWindow({
                minutes: Number(event.target.value),
                end: Date.now(),
              })
            }
          >
            <NativeSelectOption value={15}>
              {t('Last 15 minutes')}
            </NativeSelectOption>
            <NativeSelectOption value={60}>{t('Last hour')}</NativeSelectOption>
            <NativeSelectOption value={360}>
              {t('Last 6 hours')}
            </NativeSelectOption>
            <NativeSelectOption value={1440}>
              {t('Last 24 hours')}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <Button
          variant='outline'
          disabled={profile.isFetching}
          onClick={() => {
            setWindow((current) => ({ ...current, end: Date.now() }))
            props.refreshStatus()
          }}
        >
          {t('Refresh profile')}
        </Button>
      </div>
      {profile.isFetching && (
        <LoadingState className='min-h-32' message={t('Loading profile...')} />
      )}
      {profile.isError && (
        <ErrorState
          className='min-h-40'
          title={t('Unable to load profiling data')}
          description={getProfilingErrorMessage(
            profile.error,
            t,
            t('Unable to load profiling data')
          )}
          onRetry={() => void profile.refetch()}
        />
      )}
      {profile.isSuccess && !profile.isFetching && (
        <ProfileResults
          key={`${type}:${start}:${window.end}`}
          profile={profile.data}
        />
      )}
    </div>
  )
}

function InstantCapture(props: {
  status: ProfilingStatus
  refreshStatus: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const [selectedType, setSelectedType] = useState<CaptureType>('heap')
  const [selectedSeconds, setSelectedSeconds] = useState(5)
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const captureTypes = props.status.capture_types.filter(
    (type) => type !== 'cpu' || props.status.cpu_capture_available
  )
  const type = captureTypes.includes(selectedType)
    ? selectedType
    : captureTypes[0]
  const maxSeconds = Math.max(1, Math.min(15, props.status.max_capture_seconds))
  const seconds = Math.min(selectedSeconds, maxSeconds)
  const durations = [
    ...new Set([1, 5, 10, maxSeconds].filter((value) => value <= maxSeconds)),
  ].sort((a, b) => a - b)
  const capture = useMutation({
    mutationFn: (params: { profile_type: CaptureType; seconds: number }) => {
      controller.current?.abort()
      controller.current = new AbortController()
      return captureProfile(params, controller.current.signal)
    },
    onError: props.refreshStatus,
    retry: false,
    meta: { errorToast: false },
  })
  const names: Record<CaptureType, string> = {
    cpu: t('CPU'),
    heap: t('Live memory'),
    allocs: t('Allocated memory'),
    goroutine: t('Goroutines'),
    mutex: t('Mutex wait'),
    block: t('Blocking time'),
  }
  if (!props.status.pprof_enabled) {
    return (
      <EmptyState
        className='min-h-40'
        title={t('Instant capture disabled')}
        description={t(
          'Enable pprof on the server to collect profiles on demand.'
        )}
      />
    )
  }
  return (
    <div className='min-w-0 space-y-4'>
      {!props.status.cpu_capture_available && (
        <Alert>
          <AlertDescription>
            {t(
              'CPU capture is unavailable while another CPU profiler is running. Other profile types remain available.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <div className='flex flex-wrap items-end gap-3'>
        <div className='grid gap-1.5'>
          <Label htmlFor={`${id}-type`}>{t('Profile type')}</Label>
          <NativeSelect
            id={`${id}-type`}
            value={type ?? ''}
            disabled={capture.isPending || !type}
            onChange={(event) => {
              capture.reset()
              setSelectedType(event.target.value as CaptureType)
            }}
          >
            {captureTypes.map((item) => (
              <NativeSelectOption key={item} value={item}>
                {names[item]}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        {type === 'cpu' && (
          <div className='grid gap-1.5'>
            <Label htmlFor={`${id}-duration`}>{t('Capture duration')}</Label>
            <NativeSelect
              id={`${id}-duration`}
              value={seconds}
              disabled={capture.isPending}
              onChange={(event) => {
                capture.reset()
                setSelectedSeconds(Number(event.target.value))
              }}
            >
              {durations.map((duration) => (
                <NativeSelectOption key={duration} value={duration}>
                  {t('{{count}} seconds', { count: duration })}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
        )}
        <Button
          disabled={capture.isPending || !type}
          onClick={() => {
            if (type) capture.mutate({ profile_type: type, seconds })
          }}
        >
          {capture.isPending
            ? t('Collecting profile...')
            : t('Collect profile')}
        </Button>
        {capture.isPending && (
          <Button
            variant='outline'
            onClick={() => {
              controller.current?.abort()
              capture.reset()
            }}
          >
            {t('Cancel')}
          </Button>
        )}
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Capture runs once on the current gateway instance. CPU sampling lasts for the selected duration; other types return a snapshot.'
        )}
      </p>
      {capture.isPending && (
        <div role='status'>
          <LoadingState
            className='min-h-32'
            message={t('Collecting profile...')}
          />
        </div>
      )}
      {capture.isError && (
        <ErrorState
          className='min-h-40'
          title={t('Profile capture failed')}
          description={getProfilingErrorMessage(
            capture.error,
            t,
            t('Profile capture failed')
          )}
        />
      )}
      {capture.isIdle && (
        <EmptyState
          className='min-h-40'
          title={t('Ready to capture')}
          description={t(
            'Choose a profile type and start a capture to inspect this instance.'
          )}
        />
      )}
      {capture.isSuccess && (
        <ProfileResults
          key={`${capture.data.profile_type}:${capture.data.start}`}
          profile={capture.data}
        />
      )}
    </div>
  )
}
