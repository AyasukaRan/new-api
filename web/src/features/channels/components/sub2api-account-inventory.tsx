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
import { RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCountdown } from '@/hooks/use-countdown'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import {
  getServerErrorMessage,
  translateServerText,
} from '@/lib/server-error-message'

import { getSub2APIInventory, type Sub2APIAccount } from '../sub2api-api'

export function Sub2APIAccountInventory(props: {
  account: Sub2APIAccount
  userId?: number
  disabled: boolean
  onSync: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  // Match the existing per-key multiplier precision, including zero overrides.
  const ratio = new Intl.NumberFormat(locale, { maximumFractionDigits: 8 })
  const [search, setSearch] = useState('')
  const [limit, setLimit] = useState(20)
  const [tab, setTab] = useState('keys')
  const query = useQuery({
    queryKey: ['sub2api-inventory', props.userId, props.account.id],
    queryFn: ({ signal }) => getSub2APIInventory(props.account.id, signal),
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const { start, stop, isActive, secondsLeft } = useCountdown()
  const nextAttempt = query.data?.next_attempt_at ?? 0
  useEffect(() => {
    const remaining = Math.ceil(nextAttempt - Date.now() / 1000)
    if (remaining > 0) start(remaining)
    else stop()
  }, [nextAttempt, start, stop])
  const snapshot = query.data?.snapshot
  const term = search.trim().toLowerCase()
  const keys = (snapshot?.keys ?? []).filter((key) =>
    `${key.id} ${key.name} ${key.masked_key}`.toLowerCase().includes(term)
  )
  const groups = (snapshot?.groups ?? []).filter((group) =>
    `${group.id} ${group.name} ${group.platform}`.toLowerCase().includes(term)
  )
  const groupNames = new Map(
    snapshot?.groups.map((group) => [group.id, group.name])
  )
  const total = tab === 'keys' ? keys.length : groups.length
  const blocked =
    props.disabled ||
    query.isPending ||
    isActive ||
    nextAttempt > Date.now() / 1000
  return (
    <section
      id={`sub2api-inventory-${props.account.id}`}
      aria-label={t('Keys and groups')}
      className='mt-2 min-w-0 space-y-3 rounded-lg border p-3'
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='text-muted-foreground text-xs'>
          {snapshot
            ? `${t('Last synced')}: ${formatTimestampToDate(snapshot.synced_at)}`
            : t('No synced inventory')}
        </div>
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={blocked}
          onClick={props.onSync}
        >
          <RefreshCw aria-hidden='true' />
          {t('Sync now')}
        </Button>
      </div>
      {isActive && (
        <p role='status' className='text-muted-foreground text-xs'>
          {t('Try again in {{seconds}}s', { seconds: secondsLeft })}
        </p>
      )}
      {query.isPending && <LoadingState inline size='sm' />}
      {query.isError && (
        <ErrorState
          title={t('Failed to load inventory')}
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
          className='min-h-0 p-3'
        />
      )}
      {query.data?.last_error && (
        <Alert variant='destructive'>
          <AlertDescription>
            {translateServerText(t, query.data.last_error)}
          </AlertDescription>
        </Alert>
      )}
      {snapshot && (
        <Tabs
          value={tab}
          onValueChange={(value) => {
            setTab(value)
            setLimit(20)
          }}
        >
          <div className='flex flex-wrap items-center gap-2'>
            <TabsList>
              <TabsTrigger value='keys'>
                {t('API Keys')} ({formatNumber(snapshot.keys.length, locale)})
              </TabsTrigger>
              <TabsTrigger value='groups'>
                {t('Groups')} ({formatNumber(snapshot.groups.length, locale)})
              </TabsTrigger>
            </TabsList>
            <Input
              aria-label={t('Search inventory')}
              placeholder={t('Search inventory')}
              value={search}
              onChange={(event) => {
                setSearch(event.target.value)
                setLimit(20)
              }}
              className='min-w-0 basis-full sm:flex-1 sm:basis-0'
            />
          </div>
          <TabsContent value='keys'>
            <StaticDataTable
              tableClassName='min-w-[26rem]'
              tableProps={{ 'aria-label': t('API Keys') }}
              data={keys.slice(0, limit)}
              getRowKey={(key) => key.id}
              emptyContent={
                <EmptyState
                  title={t('No keys found')}
                  className='min-h-0 py-4'
                />
              }
              columns={[
                {
                  id: 'key',
                  header: t('API Key'),
                  cell: (key) => (
                    <div className='max-w-48 break-all whitespace-normal'>
                      <div>{key.name || `#${key.id}`}</div>
                      <span className='text-muted-foreground font-mono text-xs'>
                        {key.masked_key}
                      </span>
                    </div>
                  ),
                },
                {
                  id: 'group',
                  header: t('Group'),
                  cell: (key) =>
                    key.group_id === null
                      ? '—'
                      : groupNames.get(key.group_id) || `#${key.group_id}`,
                },
                {
                  id: 'status',
                  header: t('Status'),
                  cell: (key) => {
                    let label = key.status
                    if (key.status === 'active') label = t('Active')
                    else if (key.status === 'disabled') label = t('Disabled')
                    else if (key.status === 'expired') label = t('Expired')
                    return (
                      <StatusBadge
                        label={label || '—'}
                        variant={
                          key.status === 'active' ? 'success' : 'neutral'
                        }
                        copyable={false}
                      />
                    )
                  },
                },
                {
                  id: 'expires',
                  header: t('Expiration Time'),
                  cell: (key) => {
                    if (!key.expires_at) return '—'
                    const timestamp = Date.parse(key.expires_at)
                    return Number.isFinite(timestamp)
                      ? formatTimestampToDate(timestamp, 'milliseconds')
                      : '—'
                  },
                },
              ]}
            />
          </TabsContent>
          <TabsContent value='groups' className='space-y-2'>
            {groups.length === 0 && (
              <EmptyState title={t('No data')} className='min-h-0 py-4' />
            )}
            {groups.slice(0, limit).map((group) => (
              <div key={group.id} className='space-y-2 rounded-md border p-3'>
                <div className='flex flex-wrap items-center gap-2 text-sm font-medium'>
                  <span className='break-all'>
                    {group.name || `#${group.id}`}
                  </span>
                  <span className='text-muted-foreground text-xs'>
                    #{group.id} · {group.platform}
                  </span>
                </div>
                {group.description && (
                  <p className='text-muted-foreground text-xs break-words'>
                    {group.description}
                  </p>
                )}
                <dl className='grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-xs [&_dd]:text-end [&_dd]:tabular-nums'>
                  <dt>{t('Group Ratio')}</dt>
                  <dd>{ratio.format(group.group_rate_multiplier)}×</dd>
                  {group.user_rate_multiplier !== null && (
                    <>
                      <dt>{t('User Exclusive Ratio')}</dt>
                      <dd>{ratio.format(group.user_rate_multiplier)}×</dd>
                    </>
                  )}
                  <dt>{t('Upstream key multiplier')}</dt>
                  <dd>{ratio.format(group.resolved_rate_multiplier)}×</dd>
                  {group.rate && (
                    <>
                      <dt>{t('Current effective multiplier')}</dt>
                      <dd>
                        {ratio.format(group.rate.effective_rate_multiplier)}×
                      </dd>
                    </>
                  )}
                  {group.peak_rate_enabled && (
                    <>
                      <dt>{t('Peak hours')}</dt>
                      <dd className='max-w-48 break-words'>
                        {group.peak_start}–{group.peak_end}{' '}
                        {group.rate?.timezone} ·{' '}
                        {ratio.format(group.peak_rate_multiplier)}×
                      </dd>
                    </>
                  )}
                </dl>
              </div>
            ))}
          </TabsContent>
          {total > limit && (
            <Button
              type='button'
              variant='ghost'
              size='sm'
              onClick={() => setLimit(limit + 20)}
            >
              {t('Show more ({{remaining}} remaining)', {
                remaining: total - limit,
              })}
            </Button>
          )}
        </Tabs>
      )}
      {!!query.data?.records.length && (
        <Collapsible>
          <CollapsibleTrigger
            render={<Button type='button' size='sm' variant='ghost' />}
          >
            {t('Sync records')}
          </CollapsibleTrigger>
          <CollapsibleContent className='mt-2 max-h-48 space-y-2 overflow-y-auto'>
            {query.data.records.map((record) => (
              <div
                key={`${record.at}:${record.success}:${record.error_code}`}
                className='flex flex-wrap items-start gap-2 border-t pt-2 text-xs'
              >
                <span className='text-muted-foreground'>
                  {formatTimestampToDate(record.at)}
                </span>
                <StatusBadge
                  label={record.success ? t('Success') : t('Failed')}
                  variant={record.success ? 'success' : 'danger'}
                  copyable={false}
                />
                {record.success ? (
                  <span>
                    {t('API Keys')}: {formatNumber(record.keys, locale)} ·{' '}
                    {t('Groups')}: {formatNumber(record.groups, locale)}
                  </span>
                ) : (
                  <span className='text-destructive break-words'>
                    {translateServerText(t, record.error_message)}
                    {record.upstream_status > 0
                      ? ` · HTTP ${record.upstream_status}`
                      : ''}
                  </span>
                )}
              </div>
            ))}
          </CollapsibleContent>
        </Collapsible>
      )}
    </section>
  )
}
