import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable, StaticRowActions } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import {
  BuiltinSourceEditor,
  type BuiltinSource,
} from './request-source-builtin-editor'

export function BuiltinRequestSources() {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<BuiltinSource | null>(null)
  const query = useQuery({
    queryKey: ['request-source-rules', 'builtins'],
    queryFn: async ({ signal }) => {
      const response = await api.get<{
        success: boolean
        data: BuiltinSource[]
      }>('/api/option/request_source_rules/builtins', { signal })
      return requireServerSuccess(response.data).data
    },
    staleTime: 300_000,
    meta: { errorToast: false },
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  }

  const needle = search.trim().toLowerCase()
  const rows = query.data.filter(
    (row) =>
      row.name.toLowerCase().includes(needle) ||
      row.display_name.toLowerCase().includes(needle) ||
      row.examples.some((example) =>
        Object.entries(example).some(
          ([key, value]) =>
            key.toLowerCase().includes(needle) ||
            value.toLowerCase().includes(needle)
        )
      )
  )
  return (
    <div className='min-w-0 space-y-3'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'These examples illustrate built-in recognition. Custom rules take priority.'
        )}
      </p>
      <Input
        type='search'
        value={search}
        onChange={(event) => setSearch(event.target.value)}
        aria-label={t('Search sources or headers')}
        placeholder={t('Search sources or headers')}
        className='w-full sm:max-w-xs'
      />
      <StaticDataTable
        data={rows}
        getRowKey={(row) => row.name}
        emptyContent={t('No matches')}
        columns={[
          {
            id: 'name',
            header: t('Source'),
            cell: (row) => (
              <div className='space-y-0.5'>
                <span className='font-medium'>{row.name}</span>
                {row.display_name && row.display_name !== row.name && (
                  <div className='text-muted-foreground text-xs'>
                    {row.display_name}
                  </div>
                )}
              </div>
            ),
          },
          {
            id: 'examples',
            header: t('Example request headers'),
            cell: (row) => (
              <ul className='space-y-1.5 whitespace-normal'>
                {row.examples.map((example) => (
                  <li key={JSON.stringify(example)}>
                    {Object.keys(example).length
                      ? Object.entries(example).map(([name, value]) => (
                          <code key={name} className='block text-xs break-all'>
                            {name}: {value}
                          </code>
                        ))
                      : t('No client-identifying headers')}
                  </li>
                ))}
              </ul>
            ),
          },
          {
            id: 'status',
            header: t('Status'),
            cell: (row) => (
              <div>
                <Badge variant={row.enabled ? 'outline' : 'secondary'}>
                  {row.enabled ? t('Enabled') : t('Disabled')}
                </Badge>
              </div>
            ),
          },
          {
            id: 'edit',
            header: t('Actions'),
            className: 'text-right',
            cell: (row) => (
              <StaticRowActions
                editLabel={t('Edit {{title}}', { title: row.name })}
                menuLabel={t('Actions')}
                deleteLabel={t('Delete')}
                onEdit={() => setEditing(row)}
              />
            ),
          },
        ]}
      />
      {editing && (
        <BuiltinSourceEditor
          source={editing}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  )
}
