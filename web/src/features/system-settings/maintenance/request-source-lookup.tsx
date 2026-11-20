import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { formatTimestampToDate } from '@/lib/format'
import {
  getServerErrorMessage,
  getServerErrorStatus,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

type SourceLookup = {
  request_id: string
  created_at: number
  path: string
  recorded_source: string
  current_source: string
}

export function RequestSourceLookup() {
  const { t } = useTranslation()
  const [input, setInput] = useState('')
  const [submitted, setSubmitted] = useState('')
  const user = useAuthStore((state) => state.auth.user)
  const lookup = useQuery({
    queryKey: [
      'request-source-rules',
      'lookup',
      user?.id,
      user?.role,
      submitted,
    ],
    enabled: Boolean(submitted),
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
    queryFn: async ({ signal }) => {
      const result = await api.get<{ success: boolean; data: SourceLookup }>(
        '/api/option/request_source_rules/lookup',
        { params: { id: submitted }, signal }
      )
      return requireServerSuccess(result.data).data
    },
  })
  const result = lookup.data
  return (
    <div className='min-w-0 space-y-3'>
      <form
        className='flex items-center gap-2'
        onSubmit={(event) => {
          event.preventDefault()
          const id = input.trim()
          if (!id) return
          if (id === submitted) void lookup.refetch()
          else setSubmitted(id)
        }}
      >
        <Input
          aria-label={t('Request ID')}
          placeholder={t('Request ID')}
          value={input}
          maxLength={64}
          autoComplete='off'
          spellCheck={false}
          onChange={(event) => setInput(event.target.value)}
        />
        <Button type='submit' disabled={!input.trim()}>
          {t('Search')}
        </Button>
      </form>
      {lookup.isFetching && <LoadingState />}
      {lookup.isError &&
        (getServerErrorStatus(lookup.error) === 404 ? (
          <EmptyState title={t('No results found')} />
        ) : (
          <ErrorState
            description={getServerErrorMessage(lookup.error)}
            onRetry={() => void lookup.refetch()}
          />
        ))}
      {lookup.isSuccess && result && (
        <div className='space-y-3 rounded-lg border p-3'>
          <dl className='[&>dt]:text-muted-foreground grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm'>
            <dt>{t('Request ID')}</dt>
            <dd className='font-mono break-all'>{result.request_id || '—'}</dd>
            <dt>{t('Time')}</dt>
            <dd>{formatTimestampToDate(result.created_at)}</dd>
            {result.path && (
              <>
                <dt>{t('Endpoint')}</dt>
                <dd className='font-mono break-all'>{result.path}</dd>
              </>
            )}
            <dt>{t('Recorded source')}</dt>
            <dd>{result.recorded_source || t('Unidentified source')}</dd>
            <dt>{t('Current recognition')}</dt>
            <dd>{result.current_source || '—'}</dd>
          </dl>
        </div>
      )}
    </div>
  )
}
