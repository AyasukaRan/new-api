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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useModelAccessScope } from '@/hooks/use-model-access-scope'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getGroups, getGroupKeyRouting, updateGroupKeyRouting } from '../../api'
import { getMultiKeyStatusConfig, refreshChannelStatusQueries } from '../../lib'
import type { GroupKeyRoutingUpdate } from '../../types'
import { useChannels } from '../channels-provider'

type GroupKeyRoutingDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

type TierDraft = {
  revision: string
  values: Record<string, string>
}

export function GroupKeyRoutingDialog(props: GroupKeyRoutingDialogProps) {
  const scope = useModelAccessScope()
  if (!props.open) return null
  return (
    <GroupKeyRoutingContent
      key={JSON.stringify(scope)}
      onOpenChange={props.onOpenChange}
    />
  )
}

function GroupKeyRoutingContent(props: {
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const instanceId = useId()
  const { sensitiveVisible } = useChannels()
  const user = useAuthStore((state) => state.auth.user)
  const canWrite = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [selectedGroup, setSelectedGroup] = useState<string>()
  const [drafts, setDrafts] = useState<Record<string, TierDraft | undefined>>(
    {}
  )
  const [restore, setRestore] = useState<GroupKeyRoutingUpdate | null>(null)
  const groupsQuery = useQuery({
    queryKey: ['channels', 'group-key-routing-groups', instanceId],
    queryFn: async () => requireServerSuccess(await getGroups()).data ?? [],
    gcTime: 0,
  })
  const groups = groupsQuery.data ?? []
  const group =
    selectedGroup && groups.includes(selectedGroup)
      ? selectedGroup
      : (groups[0] ?? '')
  const queryKey = ['channels', 'group-key-routing', instanceId, group]
  const routingQuery = useQuery({
    queryKey,
    queryFn: ({ signal }) => getGroupKeyRouting(group, signal),
    enabled: !!group,
    gcTime: 0,
  })
  const data = routingQuery.data
  const draft = drafts[group]
  const priorities = [
    ...new Set(data?.items.map((item) => item.effective_priority) ?? []),
  ].sort((a, b) => b - a)
  const rows = (data?.items ?? []).map((item) => {
    const identity = `${item.channel_id}:${item.key_identity}`
    const savedTier = priorities.indexOf(item.effective_priority) + 1
    const value = draft?.values[identity] ?? String(savedTier)
    const tier = Number(value)
    const invalid =
      !/^\d+$/.test(value.trim()) ||
      !Number.isSafeInteger(tier) ||
      tier < 1 ||
      tier > 2147483648
    return { ...item, identity, savedTier, value, tier, invalid }
  })
  const stale = !!draft && draft.revision !== data?.revision
  const changed = rows.some((row) => row.priority !== 1 - row.tier)
  const invalid = rows.some((row) => row.invalid)
  const save = useMutation({
    mutationFn: updateGroupKeyRouting,
    onSuccess: async (result, request) => {
      client.setQueryData(
        ['channels', 'group-key-routing', instanceId, request.group],
        result
      )
      setDrafts((previous) => ({ ...previous, [request.group]: undefined }))
      setRestore(null)
      await refreshChannelStatusQueries(client)
      toast.success(t('Operation successful'))
    },
    onError: async (error) => {
      handleServerError(error, t('Operation failed'))
      await routingQuery.refetch()
    },
  })
  const busy = save.isPending || routingQuery.isFetching
  const unavailable =
    !canWrite || busy || !data || routingQuery.isError || stale
  const canRestore = !!data?.items.some((item) => item.priority !== null)

  return (
    <>
      <Dialog
        open
        onOpenChange={props.onOpenChange}
        title={t('Group failover routing')}
        description={t(
          'Set fallback tiers across channel keys. Tier 1 is tried first; keys in the same tier participate together.'
        )}
        contentClassName='sm:max-w-5xl'
        bodyClassName='flex min-w-0 flex-col gap-4'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              disabled={unavailable || !canRestore}
              onClick={() => {
                if (unavailable || !data) return
                setRestore({
                  group,
                  revision: data.revision,
                  items: data.items
                    .filter((item) => item.priority !== null)
                    .map((item) => ({
                      channel_id: item.channel_id,
                      key_identity: item.key_identity,
                      priority: null,
                    })),
                })
              }}
            >
              {t('Restore channel priorities')}
            </Button>
            <Button
              type='button'
              disabled={unavailable || !changed || invalid}
              onClick={() => {
                if (unavailable || !data || !changed || invalid) return
                // Normalize the whole group so inherited and old numeric scales cannot interleave tiers.
                const items = rows
                  .map((row) => ({
                    channel_id: row.channel_id,
                    key_identity: row.key_identity,
                    priority: 1 - row.tier,
                  }))
                  .filter(
                    (item, index) => item.priority !== rows[index].priority
                  )
                save.mutate({
                  group,
                  revision: draft?.revision ?? data.revision,
                  items,
                })
              }}
            >
              {save.isPending ? t('Saving...') : t('Save tiers')}
            </Button>
          </>
        }
      >
        <Field>
          <FieldLabel htmlFor={`${instanceId}-group`}>{t('Group')}</FieldLabel>
          <Combobox
            id={`${instanceId}-group`}
            options={groups.map((name) => ({ value: name, label: name }))}
            value={group}
            onValueChange={(value) => {
              if (value) setSelectedGroup(value)
            }}
            disabled={
              save.isPending || groupsQuery.isPending || groupsQuery.isError
            }
            placeholder={t('Select group')}
          />
        </Field>
        <FieldDescription>
          {t(
            'Saved tiers override channel priorities for this group, even if channel priorities later change. Restore channel priorities to inherit them again.'
          )}
        </FieldDescription>
        <FieldDescription>
          {t('Retries follow the global retry limit.')}
        </FieldDescription>
        {groupsQuery.isPending && <LoadingState />}
        {groupsQuery.isError && (
          <ErrorState
            title={t('Failed to load groups')}
            onRetry={() => groupsQuery.refetch()}
          />
        )}
        {groupsQuery.isSuccess && groups.length === 0 && (
          <EmptyState title={t('No groups available')} />
        )}
        {!!group && routingQuery.isPending && <LoadingState />}
        {!!group && routingQuery.isError && (
          <ErrorState
            title={t('Failed to load group routing')}
            onRetry={() => routingQuery.refetch()}
          />
        )}
        {stale && (
          <Alert>
            <AlertDescription className='flex flex-wrap items-center justify-between gap-2'>
              {t('Routing settings changed. Reload before saving.')}
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={busy}
                onClick={async () => {
                  const result = await routingQuery.refetch()
                  if (result.isSuccess) {
                    setDrafts((previous) => ({
                      ...previous,
                      [group]: undefined,
                    }))
                  }
                }}
              >
                {t('Reload routing')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {routingQuery.isSuccess && rows.length === 0 && (
          <EmptyState title={t('No eligible keys in this group')} />
        )}
        {routingQuery.isSuccess && rows.length > 0 && (
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.identity}
            tableClassName='min-w-[40rem] table-fixed'
            columns={[
              {
                id: 'channel',
                header: t('Channel'),
                cellClassName: 'whitespace-normal align-top',
                cell: (row) => (
                  <div className='flex flex-col gap-1'>
                    <span className='wrap-anywhere'>{row.channel_name}</span>
                    <span className='text-muted-foreground text-xs'>
                      #{row.channel_id}
                    </span>
                  </div>
                ),
              },
              {
                id: 'key',
                header: t('Masked key'),
                cellClassName: 'whitespace-normal align-top',
                cell: (row) => (
                  <div className='flex flex-col gap-1'>
                    <span>#{row.key_index + 1}</span>
                    <span className='font-mono text-xs break-all'>
                      {sensitiveVisible ? row.key_preview : '••••'}
                    </span>
                  </div>
                ),
              },
              {
                id: 'status',
                header: t('Status'),
                cellClassName: 'whitespace-normal align-top',
                cell: (row) => {
                  const status = getMultiKeyStatusConfig(row.status)
                  return (
                    <StatusBadge
                      label={t(status.label)}
                      variant={status.variant}
                      showDot
                      copyable={false}
                    />
                  )
                },
              },
              {
                id: 'tier',
                header: t('Failover tier'),
                className: 'w-52',
                cellClassName: 'whitespace-normal align-top',
                cell: (row) => (
                  <Field data-invalid={row.invalid} data-disabled={unavailable}>
                    <FieldLabel
                      className='sr-only'
                      htmlFor={`${instanceId}-${row.identity}`}
                    >
                      {t('Fallback tier for {{channel}} key #{{index}}', {
                        channel: row.channel_name,
                        index: row.key_index + 1,
                      })}
                    </FieldLabel>
                    <Input
                      id={`${instanceId}-${row.identity}`}
                      inputMode='numeric'
                      value={row.value}
                      aria-invalid={row.invalid}
                      disabled={unavailable}
                      onChange={(event) => {
                        if (!data) return
                        const value = event.target.value
                        setDrafts((previous) => ({
                          ...previous,
                          [group]: {
                            revision:
                              previous[group]?.revision ?? data.revision,
                            values: {
                              ...previous[group]?.values,
                              [row.identity]: value,
                            },
                          },
                        }))
                      }}
                    />
                    {row.invalid && (
                      <FieldError className='text-xs'>
                        {t(
                          'Enter a whole-number tier between 1 and 2147483648.'
                        )}
                      </FieldError>
                    )}
                    {row.priority === null && !draft && (
                      <FieldDescription className='text-xs'>
                        {t('Inherited from channel priority')}
                      </FieldDescription>
                    )}
                  </Field>
                ),
              },
            ]}
          />
        )}
      </Dialog>
      <ConfirmDialog
        open={
          restore !== null &&
          restore.group === group &&
          restore.revision === data?.revision
        }
        onOpenChange={(open) => {
          if (!open && !save.isPending) setRestore(null)
        }}
        title={t('Restore channel priorities')}
        desc={t(
          'Remove all key priority overrides in this group and use channel priorities again?'
        )}
        confirmText={t('Restore')}
        disabled={unavailable}
        isLoading={save.isPending}
        handleConfirm={() => {
          if (restore && !unavailable) save.mutate(restore)
        }}
      />
    </>
  )
}
