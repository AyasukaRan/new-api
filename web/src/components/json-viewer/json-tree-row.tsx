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
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useState, type Ref } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getJsonPath, type JsonEntry, type JsonMatch } from './json-tree-model'

const VALUE_PREVIEW_LENGTH = 500

function JsonValue(props: {
  value: string
  kind: string
  highlighted: boolean
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(props.highlighted)
  const longValue = props.value.length > VALUE_PREVIEW_LENGTH
  const displayed =
    longValue && !expanded
      ? `${props.value.slice(0, VALUE_PREVIEW_LENGTH)}…`
      : props.value
  const value = props.highlighted ? (
    <mark className='bg-warning/25 text-inherit'>{displayed}</mark>
  ) : (
    displayed
  )
  return (
    <span>
      <span
        className={cn(
          'select-text',
          props.kind === 'String' && 'text-success',
          (props.kind === 'Number' || props.kind === 'UnaryExpression') &&
            'text-warning',
          props.kind === 'BooleanLiteral' && 'text-primary',
          props.kind === 'null' && 'text-muted-foreground'
        )}
      >
        {value}
      </span>
      {longValue && (
        <Button
          className='ml-2 align-baseline'
          size='xs'
          type='button'
          variant='ghost'
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? t('Collapse value') : t('Show full value')}
        </Button>
      )}
    </span>
  )
}

export function JsonTreeRow(props: {
  code: string
  entry: JsonEntry
  expanded: boolean
  match?: JsonMatch
  rowRef?: Ref<HTMLLIElement>
  onToggle: () => void
}) {
  const { t, i18n } = useTranslation()
  const entry = props.entry
  const isContainer = entry.children.length > 0
  const isArray = entry.node.name === 'ArrayExpression'
  const opening = isArray ? '[' : '{'
  const closing = isArray ? ']' : '}'
  const label = entry.label === undefined ? null : `${entry.label}:`
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <li
      ref={props.rowRef}
      aria-current={props.match ? 'true' : undefined}
      className={cn(
        'group/json-node flex min-w-0 items-start gap-1 rounded',
        props.match && 'bg-warning/10'
      )}
      style={{ paddingLeft: `min(${entry.depth * 1.25}rem, 25%)` }}
    >
      {isContainer ? (
        <Button
          aria-expanded={props.expanded}
          aria-label={
            props.expanded
              ? t('Collapse {{name}}', { name: entry.name })
              : t('Expand {{name}}', { name: entry.name })
          }
          className='shrink-0'
          onClick={props.onToggle}
          size='icon-xs'
          type='button'
          variant='ghost'
        >
          {props.expanded ? (
            <ChevronDown aria-hidden='true' />
          ) : (
            <ChevronRight aria-hidden='true' />
          )}
        </Button>
      ) : (
        <span className='size-6 shrink-0' aria-hidden='true' />
      )}
      <div className='min-w-0 flex-1 py-0.5 wrap-anywhere whitespace-pre-wrap'>
        {label !== null && (
          <span className='text-info select-text'>
            {props.match?.key ? (
              <mark className='bg-warning/25 text-inherit'>{label}</mark>
            ) : (
              label
            )}
          </span>
        )}
        {label !== null && ' '}
        {isContainer ? (
          <span className='text-muted-foreground'>
            {opening}
            {!props.expanded && ` … ${closing}`}
            <span className='ml-2 text-xs'>
              ({formatNumber(entry.children.length, locale)})
            </span>
          </span>
        ) : (
          <JsonValue
            key={String(Boolean(props.match?.value))}
            kind={entry.node.name}
            value={props.code.slice(entry.node.from, entry.node.to)}
            highlighted={Boolean(props.match?.value)}
          />
        )}
      </div>
      <div className='flex shrink-0 flex-col items-end opacity-60 transition-opacity group-hover/json-node:opacity-100 focus-within:opacity-100 sm:flex-row sm:items-center'>
        <CopyButton
          className='h-6 px-1.5'
          iconClassName='size-3'
          size='sm'
          value={props.code.slice(entry.node.from, entry.node.to)}
          tooltip={t('Copy node JSON')}
        >
          JSON
        </CopyButton>
        <CopyButton
          className='h-6 px-1.5'
          iconClassName='size-3'
          size='sm'
          value={getJsonPath(entry)}
          tooltip={t('Copy JSON path')}
        >
          {t('Path')}
        </CopyButton>
      </div>
    </li>
  )
}
