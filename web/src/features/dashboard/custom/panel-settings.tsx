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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import {
  CHART_LABELS,
  DIMENSION_LABELS,
  GRANULARITY_LABELS,
  METRIC_LABELS,
  RANGE_LABELS,
  SOURCE_LABELS,
  getPanelCapabilities,
  isPanelAllowed,
} from './registry'
import type { BoardTime, DashboardPanel, PanelSource } from './types'

function BoardChoice<T extends string>(props: {
  label: string
  value: T
  values: readonly T[]
  labels: Record<T, string>
  onChange: (value: T) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <Field className='min-w-0'>
      <FieldLabel htmlFor={id}>{props.label}</FieldLabel>
      <Select
        value={props.value}
        items={props.values.map((value) => ({
          value,
          label: t(props.labels[value]),
        }))}
        onValueChange={(value) => {
          if (value !== null) props.onChange(value as T)
        }}
        disabled={props.disabled}
      >
        <SelectTrigger id={id} className='w-full'>
          <SelectValue />
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          {props.values.map((value) => (
            <SelectItem key={value} value={value}>
              {t(props.labels[value])}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

export function BoardTimeControls(props: {
  value: BoardTime
  onChange: (value: BoardTime) => void
  source?: PanelSource
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const capability = props.source
    ? getPanelCapabilities(props.source, true)
    : {
        ranges: Object.keys(RANGE_LABELS) as BoardTime['range'][],
        granularities: Object.keys(
          GRANULARITY_LABELS
        ) as BoardTime['granularity'][],
        refreshIntervals: [0, 5, 15, 30, 60] as BoardTime['refresh'][],
      }
  return (
    <div className='grid min-w-0 gap-3 sm:grid-cols-3'>
      <BoardChoice
        label={t('Time range')}
        value={props.value.range}
        values={capability.ranges}
        labels={RANGE_LABELS}
        disabled={props.disabled}
        onChange={(range) => props.onChange({ ...props.value, range })}
      />
      <BoardChoice
        label={t('Time granularity')}
        value={props.value.granularity}
        values={capability.granularities}
        labels={GRANULARITY_LABELS}
        disabled={props.disabled}
        onChange={(granularity) =>
          props.onChange({ ...props.value, granularity })
        }
      />
      <BoardChoice
        label={t('Auto refresh')}
        value={String(props.value.refresh)}
        values={capability.refreshIntervals.map(String)}
        labels={Object.fromEntries(
          capability.refreshIntervals.map((value) => [
            String(value),
            value ? `${value}s` : 'Off',
          ])
        )}
        disabled={props.disabled}
        onChange={(value) =>
          props.onChange({
            ...props.value,
            refresh: Number(value) as BoardTime['refresh'],
          })
        }
      />
    </div>
  )
}

export function DashboardPanelSettings(props: {
  panel: DashboardPanel
  globalTime: BoardTime
  isAdmin: boolean
  onApply: (panel: DashboardPanel) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const [draft, setDraft] = useState<DashboardPanel>(() =>
    structuredClone(props.panel)
  )
  const capability = getPanelCapabilities(draft.source, props.isAdmin)
  const charts = capability.charts.filter(
    (chart) =>
      chart !== 'pie' || (draft.source !== 'live' && draft.metric !== 'profit')
  )
  const dimensions =
    draft.metric === 'concurrency'
      ? (['total'] as const)
      : capability.dimensions
  const changeSource = (source: PanelSource) => {
    const next = getPanelCapabilities(source, props.isAdmin)
    setDraft((old) => ({
      ...old,
      source,
      metric: next.metrics.includes(old.metric) ? old.metric : next.metrics[0],
      dimension: next.dimensions.includes(old.dimension)
        ? old.dimension
        : next.dimensions[0],
      chart:
        next.charts.includes(old.chart) && old.chart !== 'pie'
          ? old.chart
          : 'line',
      filters: {
        ...(source !== 'sources' && old.filters.model
          ? { model: old.filters.model }
          : {}),
        ...(props.isAdmin && source !== 'live' && old.filters.username
          ? { username: old.filters.username }
          : {}),
      },
      ...(old.time
        ? {
            time: {
              range: next.ranges.includes(old.time.range)
                ? old.time.range
                : '24h',
              granularity: next.granularities.includes(old.time.granularity)
                ? old.time.granularity
                : 'auto',
              refresh: next.refreshIntervals.includes(old.time.refresh)
                ? old.time.refresh
                : 0,
            },
          }
        : {}),
    }))
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Panel settings')}
      contentClassName='sm:max-w-xl'
      footer={
        <>
          <Button variant='outline' onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={!isPanelAllowed(draft, props.isAdmin)}
            onClick={() =>
              props.onApply({
                ...draft,
                title: draft.title.trim(),
                filters: {
                  ...(draft.filters.model?.trim()
                    ? { model: draft.filters.model.trim() }
                    : {}),
                  ...(draft.filters.username?.trim()
                    ? { username: draft.filters.username.trim() }
                    : {}),
                },
              })
            }
          >
            {t('Apply')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <Field>
          <FieldLabel htmlFor={`${id}-title`}>{t('Title')}</FieldLabel>
          <Input
            id={`${id}-title`}
            value={draft.title}
            maxLength={80}
            onChange={(event) =>
              setDraft({ ...draft, title: event.target.value })
            }
          />
        </Field>
        <div className='grid gap-3 sm:grid-cols-2'>
          <BoardChoice
            label={t('Data source')}
            value={draft.source}
            values={(Object.keys(SOURCE_LABELS) as PanelSource[]).filter(
              (source) =>
                getPanelCapabilities(source, props.isAdmin).metrics.length > 0
            )}
            labels={SOURCE_LABELS}
            onChange={changeSource}
          />
          <BoardChoice
            label={t('Metric')}
            value={draft.metric}
            values={capability.metrics}
            labels={METRIC_LABELS}
            onChange={(metric) =>
              setDraft({
                ...draft,
                metric,
                filters: metric === 'concurrency' ? {} : draft.filters,
                dimension: metric === 'concurrency' ? 'total' : draft.dimension,
                chart:
                  (metric === 'profit' || draft.source === 'live') &&
                  draft.chart === 'pie'
                    ? 'line'
                    : draft.chart,
              })
            }
          />
          <BoardChoice
            label={t('Chart type')}
            value={draft.chart}
            values={charts}
            labels={CHART_LABELS}
            onChange={(chart) => setDraft({ ...draft, chart })}
          />
          <BoardChoice
            label={t('Group by')}
            value={draft.dimension}
            values={dimensions}
            labels={DIMENSION_LABELS}
            onChange={(dimension) => setDraft({ ...draft, dimension })}
          />
          {draft.source !== 'sources' && draft.metric !== 'concurrency' && (
            <Field>
              <FieldLabel htmlFor={`${id}-model`}>{t('Model Name')}</FieldLabel>
              <Input
                id={`${id}-model`}
                maxLength={128}
                value={draft.filters.model ?? ''}
                placeholder={t('All models')}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    filters: { ...draft.filters, model: event.target.value },
                  })
                }
              />
            </Field>
          )}
          {props.isAdmin && draft.source !== 'live' && (
            <Field>
              <FieldLabel htmlFor={`${id}-user`}>{t('Username')}</FieldLabel>
              <Input
                id={`${id}-user`}
                maxLength={64}
                value={draft.filters.username ?? ''}
                placeholder={t('All users')}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    filters: { ...draft.filters, username: event.target.value },
                  })
                }
              />
            </Field>
          )}
        </div>
        <Field orientation='horizontal'>
          <FieldLabel htmlFor={`${id}-time`}>
            {t('Use panel time range')}
          </FieldLabel>
          <Switch
            id={`${id}-time`}
            checked={!!draft.time}
            onCheckedChange={(checked) =>
              setDraft({
                ...draft,
                time: checked
                  ? {
                      range: capability.ranges.includes(props.globalTime.range)
                        ? props.globalTime.range
                        : '24h',
                      granularity: capability.granularities.includes(
                        props.globalTime.granularity
                      )
                        ? props.globalTime.granularity
                        : 'auto',
                      refresh: capability.refreshIntervals.includes(
                        props.globalTime.refresh
                      )
                        ? props.globalTime.refresh
                        : 0,
                    }
                  : undefined,
              })
            }
          />
        </Field>
        {draft.time && (
          <BoardTimeControls
            source={draft.source}
            value={draft.time}
            onChange={(time) => setDraft({ ...draft, time })}
          />
        )}
        {!draft.time && (
          <p className='text-muted-foreground text-xs'>
            {t('Follows the dashboard time range and refresh interval.')}
          </p>
        )}
      </div>
    </Dialog>
  )
}
