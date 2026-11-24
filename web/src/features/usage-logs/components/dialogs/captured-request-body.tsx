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
import { ChevronDown, FileJson } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getLogRequestBody } from '../../api'
import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'

/** Historical logs without a trace keep their sole payload behind an explicit disclosure. */
export function CapturedRequestBody(props: {
  log: UsageLog
  adminInfo: NonNullable<LogOtherData['admin_info']>
}) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const [expanded, setExpanded] = useState(false)
  if ((user?.role ?? 0) < ROLE.ADMIN) return null
  return (
    <Collapsible
      open={expanded}
      onOpenChange={setExpanded}
      className='bg-muted/65 ring-border/50 dark:bg-foreground/[0.045] min-w-0 rounded-xl ring-1 ring-inset'
    >
      <CollapsibleTrigger className='group flex w-full items-center justify-between gap-3 px-3 py-3 text-left text-sm font-semibold'>
        <span className='flex items-center gap-1.5'>
          <FileJson className='size-3.5' aria-hidden='true' />
          {t('Request Content')}
        </span>
        <ChevronDown
          className='size-3.5 transition-transform group-data-[panel-open]:rotate-180'
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <CollapsibleContent className='min-w-0 px-2.5 pb-2.5'>
        {expanded && <LegacyCapturedRequestBody {...props} />}
      </CollapsibleContent>
    </Collapsible>
  )
}

function LegacyCapturedRequestBody(props: {
  log: UsageLog
  adminInfo: NonNullable<LogOtherData['admin_info']>
}) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const {
    request_body: inlineBody,
    request_body_ref: ref,
    request_body_truncated,
  } = props.adminInfo
  const { created_at, request_id = '' } = props.log
  const canLoad = (user?.role ?? 0) >= ROLE.ADMIN
  const query = useQuery({
    queryKey: [
      'log-request-body',
      user?.id,
      user?.role,
      created_at,
      request_id,
      ref,
    ],
    queryFn: ({ signal }) =>
      getLogRequestBody(created_at, request_id, ref ?? '', signal),
    enabled: canLoad && !inlineBody && !!ref,
    retry: false,
    gcTime: 0,
  })
  if (!canLoad) return null
  const body = inlineBody ?? query.data
  return (
    <>
      {body != null && (
        <div className='bg-muted/50 relative rounded-md border p-3'>
          <CopyButton value={body} className='absolute top-2 right-2 size-8' />
          <pre className='max-h-72 overflow-auto pr-10 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap'>
            {body}
          </pre>
        </div>
      )}
      {body == null && query.isError && (
        <ErrorState
          className='min-h-0'
          title={t('Failed to load logs')}
          onRetry={() => void query.refetch()}
        />
      )}
      {body == null && !query.isError && (
        <LoadingState inline message={t('Loading...')} />
      )}
      {request_body_truncated != null && (
        <p className='text-muted-foreground mt-2 text-xs'>
          {t(
            'Truncated for storage. The original request was {{bytes}} bytes.',
            { bytes: request_body_truncated }
          )}
        </p>
      )}
    </>
  )
}
