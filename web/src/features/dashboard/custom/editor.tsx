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
import { ArrowLeft, Pencil, Plus, Save, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  getServerErrorMessage,
  getServerErrorStatus,
} from '@/lib/server-error-message'

import { getDashboardConfig, saveDashboardConfig } from './api'
import { DashboardGrid } from './grid'
import { placeDashboardPanel } from './layout'
import { BoardTimeControls, DashboardPanelSettings } from './panel-settings'
import { createDefaultDashboard, createDefaultPanel } from './registry'
import type {
  DashboardConfig,
  DashboardPanel,
  PersonalDashboard,
} from './types'

export function CustomDashboardEditor(props: {
  userId: number
  isAdmin: boolean
  onExit: () => void
}) {
  return (
    <DashboardEditorSession
      key={`${props.userId}:${props.isAdmin}`}
      {...props}
    />
  )
}

function DashboardEditorSession(props: {
  userId: number
  isAdmin: boolean
  onExit: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const queryKey = ['dashboard-config', props.userId, props.isAdmin]
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => getDashboardConfig(signal),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const [fallback] = useState<DashboardConfig>(() => {
    const board = createDefaultDashboard(props.isAdmin)
    board.name = t('My dashboard')
    return { version: 1, active: board.id, boards: [board] }
  })
  const [draft, setDraft] = useState<DashboardConfig | null>(null)
  const [revision, setRevision] = useState(0)
  const [selected, setSelected] = useState('')
  const [viewTimes, setViewTimes] = useState<
    Record<string, PersonalDashboard['time']>
  >({})
  const [panelSettings, setPanelSettings] = useState<DashboardPanel | null>(
    null
  )
  const [naming, setNaming] = useState<'new' | 'rename' | null>(null)
  const [name, setName] = useState('')
  const [confirmation, setConfirmation] = useState<
    'cancel' | 'exit' | 'delete' | 'reload' | null
  >(null)
  const [conflict, setConflict] = useState(false)
  const mutation = useMutation({
    mutationFn: (value: DashboardConfig) =>
      saveDashboardConfig(revision, value),
    retry: false,
    meta: { errorToast: false },
    onSuccess: (response) => {
      client.setQueryData(queryKey, response)
      setDraft(null)
      setViewTimes({})
      setConflict(false)
      toast.success(t('Saved successfully'))
    },
  })
  const config = draft ?? query.data?.config ?? fallback
  const board =
    config.boards.find((item) => item.id === selected) ??
    config.boards.find((item) => item.id === config.active) ??
    config.boards[0]
  const effectiveTime =
    board && !draft ? (viewTimes[board.id] ?? board.time) : board?.time
  const begin = () => {
    const next = structuredClone(config)
    if (board && effectiveTime) {
      next.boards = next.boards.map((item) =>
        item.id === board.id ? { ...item, time: effectiveTime } : item
      )
    }
    setDraft(next)
    setRevision(query.data?.revision ?? 0)
    setConflict(false)
    mutation.reset()
  }
  const updateBoard = (change: Partial<PersonalDashboard>) => {
    if (!board || !draft) return
    setDraft({
      ...draft,
      boards: draft.boards.map((item) =>
        item.id === board.id ? { ...item, ...change } : item
      ),
    })
  }
  const save = async () => {
    if (!draft) return
    try {
      await mutation.mutateAsync({ ...draft, active: board?.id ?? '' })
    } catch (error) {
      setConflict(getServerErrorStatus(error) === 409)
    }
  }
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load dashboard')}
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <div className='dashboard-workbench space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex min-w-0 flex-wrap items-center gap-2'>
          <Button
            variant='ghost'
            size='sm'
            disabled={mutation.isPending}
            onClick={() => (draft ? setConfirmation('exit') : props.onExit())}
          >
            <ArrowLeft />
            {t('Built-in dashboard')}
          </Button>
          <Select
            value={board?.id ?? ''}
            items={config.boards.map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            disabled={mutation.isPending}
            onValueChange={(value) => {
              if (value !== null) setSelected(value)
            }}
          >
            <SelectTrigger aria-label={t('Dashboard')} className='max-w-64'>
              <SelectValue placeholder={t('Choose a dashboard')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              {config.boards.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {draft && (
            <>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Rename dashboard')}
                disabled={!board || mutation.isPending}
                onClick={() => {
                  setName(board?.name ?? '')
                  setNaming('rename')
                }}
              >
                <Pencil />
              </Button>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Delete dashboard')}
                disabled={
                  !board || config.boards.length <= 1 || mutation.isPending
                }
                onClick={() => setConfirmation('delete')}
              >
                <Trash2 />
              </Button>
              <Button
                variant='outline'
                size='sm'
                disabled={config.boards.length >= 10 || mutation.isPending}
                onClick={() => {
                  setName('')
                  setNaming('new')
                }}
              >
                <Plus />
                {t('New dashboard')}
              </Button>
            </>
          )}
        </div>
        <div className='flex flex-wrap gap-2'>
          {draft ? (
            <>
              <Button
                variant='outline'
                size='sm'
                disabled={
                  !board || board.panels.length >= 24 || mutation.isPending
                }
                onClick={() => {
                  const panel = createDefaultPanel()
                  panel.layout.y = Math.max(
                    0,
                    ...board.panels.map((item) => item.layout.y + item.layout.h)
                  )
                  if (panel.layout.y > 4096) panel.layout.y = 0
                  setPanelSettings(panel)
                }}
              >
                <Plus />
                {t('Add panel')}
              </Button>
              <Button
                variant='outline'
                size='sm'
                disabled={mutation.isPending}
                onClick={() => setConfirmation('cancel')}
              >
                {t('Cancel')}
              </Button>
              <Button
                size='sm'
                disabled={mutation.isPending}
                onClick={() => void save()}
              >
                <Save />
                {t('Save dashboard')}
              </Button>
            </>
          ) : (
            <Button variant='outline' size='sm' onClick={begin}>
              <Pencil />
              {t('Edit dashboard')}
            </Button>
          )}
        </div>
      </div>
      {draft && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Editing draft. Changes are saved only when you save the dashboard.'
          )}
        </p>
      )}
      {mutation.isError && (
        <Alert variant='destructive'>
          <AlertDescription>
            {conflict
              ? t(
                  'This dashboard changed elsewhere. Your draft is preserved. Reload the saved version or keep editing.'
                )
              : getServerErrorMessage(mutation.error)}
            {conflict && (
              <Button
                variant='outline'
                size='sm'
                className='mt-2'
                onClick={() => setConfirmation('reload')}
              >
                {t('Reload saved version')}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      {board && (
        <>
          <BoardTimeControls
            value={effectiveTime ?? board.time}
            disabled={mutation.isPending}
            onChange={(time) =>
              draft
                ? updateBoard({ time })
                : setViewTimes({ ...viewTimes, [board.id]: time })
            }
          />
          {board.panels.length === 0 && (
            <EmptyState
              title={t('No panels')}
              description={t(
                'Add a panel in edit mode to start this dashboard.'
              )}
            />
          )}
          <DashboardGrid
            panels={board.panels}
            time={effectiveTime ?? board.time}
            editing={!!draft && !mutation.isPending}
            isAdmin={props.isAdmin}
            userId={props.userId}
            onChange={(panels) => updateBoard({ panels })}
            onConfigure={setPanelSettings}
            onRemove={(panel) =>
              updateBoard({
                panels: board.panels.filter((item) => item.id !== panel.id),
              })
            }
          />
        </>
      )}
      {!board && (
        <EmptyState
          title={t('No dashboards')}
          description={t('Create a dashboard in edit mode.')}
        />
      )}
      {board && panelSettings && (
        <DashboardPanelSettings
          key={panelSettings.id}
          panel={panelSettings}
          globalTime={board.time}
          isAdmin={props.isAdmin}
          onClose={() => setPanelSettings(null)}
          onApply={(panel) => {
            updateBoard({
              panels: board.panels.some((item) => item.id === panel.id)
                ? board.panels.map((item) =>
                    item.id === panel.id ? panel : item
                  )
                : placeDashboardPanel(
                    [...board.panels, panel],
                    panel.id,
                    panel.layout
                  ),
            })
            setPanelSettings(null)
          }}
        />
      )}
      <Dialog
        open={naming !== null}
        onOpenChange={(open) => {
          if (!open) setNaming(null)
        }}
        title={naming === 'new' ? t('New dashboard') : t('Rename dashboard')}
        contentClassName='sm:max-w-sm'
        footer={
          <>
            <Button variant='outline' onClick={() => setNaming(null)}>
              {t('Cancel')}
            </Button>
            <Button
              disabled={!name.trim()}
              onClick={() => {
                if (!draft) return
                if (naming === 'rename') updateBoard({ name: name.trim() })
                else {
                  const created = createDefaultDashboard(props.isAdmin)
                  created.name = name.trim()
                  setDraft({
                    ...draft,
                    active: created.id,
                    boards: [...draft.boards, created],
                  })
                  setSelected(created.id)
                }
                setNaming(null)
              }}
            >
              {t('Apply')}
            </Button>
          </>
        }
      >
        <div className='space-y-2'>
          <Label htmlFor='dashboard-name'>{t('Name')}</Label>
          <Input
            id='dashboard-name'
            autoFocus
            value={name}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
          />
        </div>
      </Dialog>
      <ConfirmDialog
        open={confirmation !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmation(null)
        }}
        title={
          confirmation === 'delete'
            ? t('Delete dashboard')
            : t('Discard unsaved changes?')
        }
        desc={
          confirmation === 'delete'
            ? t(
                'This dashboard will be removed when you save. Other dashboards stay unchanged.'
              )
            : t('Your unsaved dashboard changes will be discarded.')
        }
        destructive
        confirmText={confirmation === 'delete' ? t('Delete') : t('Discard')}
        handleConfirm={() => {
          if (confirmation === 'delete' && draft && board) {
            const boards = draft.boards.filter((item) => item.id !== board.id)
            setDraft({ ...draft, boards, active: boards[0]?.id ?? '' })
            setSelected(boards[0]?.id ?? '')
          } else {
            setDraft(null)
            setConflict(false)
            mutation.reset()
            if (confirmation === 'exit') props.onExit()
            if (confirmation === 'reload') void query.refetch()
          }
          setConfirmation(null)
        }}
      />
    </div>
  )
}
