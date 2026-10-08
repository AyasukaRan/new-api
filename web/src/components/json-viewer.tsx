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
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlock, CodeBlockFrame } from '@/components/ai-elements/code-block'
import { CopyButton } from '@/components/copy-button'
import {
  getJsonChildren,
  parseJsonSource,
  type JsonSyntaxNode,
} from '@/components/json-viewer/json-source'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

const CHILD_BATCH_SIZE = 50
const VALUE_PREVIEW_LENGTH = 500

type JsonViewerProps = {
  code: string
  title?: string
}

type JsonTreeNodeProps = {
  code: string
  depth: number
  label?: string
  name?: string
  node: JsonSyntaxNode
}

function JsonValue(props: { value: string; kind: string }) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const longValue = props.value.length > VALUE_PREVIEW_LENGTH
  const displayed =
    longValue && !expanded
      ? `${props.value.slice(0, VALUE_PREVIEW_LENGTH)}…`
      : props.value

  return (
    <span className='min-w-0 break-all whitespace-pre-wrap'>
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
        {displayed}
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

function JsonTreeNode(props: JsonTreeNodeProps) {
  const { t, i18n } = useTranslation()
  const childrenId = useId()
  const [expanded, setExpanded] = useState(props.depth < 2)
  const [visibleCount, setVisibleCount] = useState(CHILD_BATCH_SIZE)
  const children = useMemo(
    () => getJsonChildren(props.node, props.code),
    [props.node, props.code]
  )
  const isArray = props.node.name === 'ArrayExpression'
  const opening = isArray ? '[' : '{'
  const closing = isArray ? ']' : '}'
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const name = props.name ?? '$'
  const key = props.label !== undefined && (
    <span className='text-info break-all whitespace-pre-wrap select-text'>
      {props.label}:
    </span>
  )

  if (children.length === 0) {
    let value = props.code.slice(props.node.from, props.node.to)
    if (props.node.name === 'ObjectExpression' || isArray) {
      value = opening + closing
    }
    return (
      <li className='flex min-w-0 gap-2 py-0.5 pl-6'>
        {key}
        <JsonValue kind={props.node.name} value={value} />
      </li>
    )
  }

  return (
    <li className='min-w-0'>
      <div className='flex min-w-0 items-start gap-1'>
        <Button
          aria-controls={childrenId}
          aria-expanded={expanded}
          aria-label={
            expanded
              ? t('Collapse {{name}}', { name })
              : t('Expand {{name}}', { name })
          }
          className='shrink-0'
          onClick={() => setExpanded((value) => !value)}
          size='icon-xs'
          type='button'
          variant='ghost'
        >
          {expanded ? (
            <ChevronDown aria-hidden='true' />
          ) : (
            <ChevronRight aria-hidden='true' />
          )}
        </Button>
        <div className='min-w-0 py-0.5'>
          {key}{' '}
          <span className='text-muted-foreground'>
            {opening}
            {!expanded && ' … '}
            {!expanded && closing}
            <span className='ml-2 text-xs'>
              ({formatNumber(children.length, locale)})
            </span>
          </span>
        </div>
      </div>
      {expanded && (
        <div id={childrenId} className='ml-3 border-l pl-3'>
          <ul className='m-0 list-none p-0'>
            {children.slice(0, visibleCount).map((child) => (
              <JsonTreeNode
                key={child.node.from}
                code={props.code}
                depth={props.depth + 1}
                {...child}
              />
            ))}
          </ul>
          {visibleCount < children.length && (
            <Button
              className='my-1'
              size='xs'
              type='button'
              variant='ghost'
              onClick={() =>
                setVisibleCount((count) => count + CHILD_BATCH_SIZE)
              }
            >
              {t('Show more ({{remaining}} remaining)', {
                remaining: formatNumber(children.length - visibleCount, locale),
              })}
            </Button>
          )}
          <div className='text-muted-foreground py-0.5'>{closing}</div>
        </div>
      )}
    </li>
  )
}

export function JsonViewer(props: JsonViewerProps) {
  const { t } = useTranslation()
  const [raw, setRaw] = useState(false)
  const root = useMemo(() => parseJsonSource(props.code), [props.code])
  const actions = (
    <>
      {root && (
        <Button
          size='xs'
          type='button'
          variant='ghost'
          onClick={() => setRaw((value) => !value)}
        >
          {raw ? t('JSON tree') : t('Raw JSON')}
        </Button>
      )}
      <CopyButton value={props.code} tooltip={t('Copy JSON')} />
    </>
  )

  if (!root || raw) {
    return (
      <CodeBlock
        code={props.code}
        language='json'
        title={props.title ?? 'JSON'}
        showToolbar
        showLineNumbers
        maxExpandedLines={20}
      >
        {actions}
      </CodeBlock>
    )
  }

  return (
    <CodeBlockFrame
      bodyClassName='p-3 font-mono text-xs leading-6'
      bodyMaxHeight='32rem'
      endActions={actions}
      showToolbar
      title={props.title ?? 'JSON'}
    >
      <ul aria-label={t('JSON tree')} className='m-0 list-none p-0'>
        <JsonTreeNode code={props.code} depth={0} node={root} />
      </ul>
    </CodeBlockFrame>
  )
}
