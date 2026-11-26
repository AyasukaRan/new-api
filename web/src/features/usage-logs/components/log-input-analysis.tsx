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
import { ChevronDown } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getLogInputAnalysis } from '../api'
import type { UsageLog } from '../data/schema'
import type { InputCategory, LogOtherData } from '../types'
import { LogBreakdownBar } from './log-breakdown-bar'

interface InputAnalysisProps {
  log: UsageLog
  admin: NonNullable<LogOtherData['admin_info']>
  path?: string
}

export function LogInputAnalysis(props: InputAnalysisProps) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const [open, setOpen] = useState(false)
  if (!user || user.role < ROLE.ADMIN) return null
  return (
    <Collapsible open={open} onOpenChange={setOpen} className='min-w-0'>
      <CollapsibleTrigger className='group flex w-full items-center justify-between gap-2 py-2 text-start text-sm font-medium'>
        {t('Input composition')}
        <ChevronDown
          aria-hidden='true'
          className='text-muted-foreground size-4 shrink-0 group-aria-expanded:rotate-180'
        />
      </CollapsibleTrigger>
      <CollapsibleContent>
        {open && (
          <RecordedInputAnalysis {...props} userId={user.id} role={user.role} />
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

function RecordedInputAnalysis(
  props: InputAnalysisProps & { userId: number; role: number }
) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { log, admin, path } = props
  const recorded = Boolean(
    admin.trace_id || admin.request_body_ref || admin.request_body
  )
  const query = useQuery({
    queryKey: [
      'usage-logs',
      'input-analysis',
      props.userId,
      props.role,
      log.id,
      log.created_at,
      log.request_id,
      admin.trace_id,
      admin.request_body_ref,
    ],
    queryFn: ({ signal }) => getLogInputAnalysis(log, admin, path, signal),
    enabled: recorded,
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    meta: { errorToast: false },
  })
  if (query.isError) {
    return (
      <ErrorState
        className='min-h-0'
        title={t('Failed to load the recorded input')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (recorded && !query.data) {
    return <LoadingState inline message={t('Loading...')} />
  }
  const data = query.data
  const issues = data?.reasons ?? []
  if (
    !data ||
    (data.status === 'unavailable' && issues.includes('not_recorded'))
  ) {
    return (
      <p className='text-muted-foreground py-2 text-xs'>
        {t('No input content was retained for this request.')}
      </p>
    )
  }
  const names: Record<InputCategory, [string, string]> = {
    system: [t('System prompt'), 'bg-sky-500'],
    developer: [t('Developer instructions'), 'bg-violet-500'],
    user: [t('User messages'), 'bg-teal-500'],
    assistant: [t('Assistant history'), 'bg-blue-500'],
    tool_results: [t('Tool results'), 'bg-amber-500'],
    tool_definitions: [t('Tool definitions'), 'bg-pink-500'],
  }
  const rows = (Object.keys(names) as InputCategory[]).flatMap((key) => {
    const category = data.categories?.[key]
    if (!category || !category.count) return []
    return [
      {
        key,
        label: names[key][0],
        color: names[key][1],
        count: category.count,
        value: category.estimated_tokens,
      },
    ]
  })
  const reasons: Record<string, string> = {
    truncated: t('Only part of the request was retained.'),
    unsupported: t('This input format could not be analyzed.'),
    referenced_history: t('Referenced history is outside this request.'),
    non_text: t('Media content is excluded from the text estimate.'),
    unrecognized_content: t('Some input blocks could not be classified.'),
  }
  return (
    <div className='min-w-0 space-y-2 pb-1'>
      {rows.length > 0 && (
        <>
          <div className='text-muted-foreground flex flex-wrap items-baseline justify-between gap-x-3 text-xs'>
            <span>{t('Locally estimated text tokens')}</span>
            <span className='text-foreground font-semibold tabular-nums'>
              ≈ {formatNumber(data.estimated_tokens, locale)}
            </span>
          </div>
          <LogBreakdownBar
            label={t('Input composition')}
            total={data.estimated_tokens}
            segments={rows}
          />
          <div className='grid grid-cols-[minmax(0,1fr)_auto_auto] items-baseline gap-x-3 text-xs'>
            <span />
            <span className='text-muted-foreground text-end'>{t('Items')}</span>
            <span className='text-muted-foreground text-end'>
              {t('Estimated tokens')}
            </span>
            {rows.map(({ key, label, color, count, value }) => (
              <div
                key={key}
                className='col-span-full grid grid-cols-subgrid items-center py-1.5 text-start'
              >
                <span className='flex min-w-0 items-center gap-1.5'>
                  <span
                    aria-hidden='true'
                    className={`size-2 shrink-0 rounded-full ${color}`}
                  />
                  {label}
                </span>
                <span className='text-muted-foreground text-end tabular-nums'>
                  {formatNumber(count, locale)}
                </span>
                <span className='text-end tabular-nums'>
                  {formatNumber(value, locale)}
                </span>
              </div>
            ))}
          </div>
        </>
      )}
      {issues.map(
        (reason) =>
          reasons[reason] && (
            <p key={reason} className='text-muted-foreground text-xs'>
              {reasons[reason]}
            </p>
          )
      )}
      {!rows.length && !issues.some((reason) => reasons[reason]) && (
        <p className='text-muted-foreground text-xs'>
          {t('No text input was recorded.')}
        </p>
      )}
    </div>
  )
}
