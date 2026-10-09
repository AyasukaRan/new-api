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
import { ChevronDown, ChevronUp } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { JsonSyntaxNode } from './json-source'
import {
  CHILD_BATCH_SIZE,
  NODE_BATCH_SIZE,
  getVisibleJsonRows,
  indexJsonTree,
  searchJsonTree,
  type JsonPage,
} from './json-tree-model'
import { JsonTreeRow } from './json-tree-row'

export function JsonTree(props: { code: string; root: JsonSyntaxNode }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [query, setQuery] = useState('')
  const [matchIndex, setMatchIndex] = useState(0)
  const [mode, setMode] = useState<boolean | undefined>()
  const [expanded, setExpanded] = useState(new Map<number, boolean>())
  const [pages, setPages] = useState(new Map<number, JsonPage>())
  const [limit, setLimit] = useState(NODE_BATCH_SIZE)
  const activeRef = useRef<HTMLLIElement>(null)
  const viewportRef = useRef<HTMLDivElement>(null)
  const entries = useMemo(
    () => indexJsonTree(props.root, props.code),
    [props.root, props.code]
  )
  const matches = useMemo(
    () => searchJsonTree(entries, props.code, query),
    [entries, props.code, query]
  )
  const match = matches[matchIndex]
  const view = useMemo(
    () =>
      getVisibleJsonRows({
        root: entries[0],
        expanded,
        mode,
        pages,
        active: match?.entry,
        limit,
      }),
    [entries, expanded, mode, pages, match, limit]
  )

  useEffect(() => {
    const viewport = viewportRef.current
    const active = activeRef.current
    if (!viewport || !active) return
    const bounds = viewport.getBoundingClientRect()
    const target = active.getBoundingClientRect()
    // The enclosing conversation can clip the bottom of this viewport.
    // Keep the match at the top without scrolling the conversation or page.
    viewport.scrollTop += target.top - bounds.top
  }, [view])

  function resetView() {
    setExpanded(new Map())
    setPages(new Map())
    setLimit(NODE_BATCH_SIZE)
    setMode(undefined)
  }

  function navigateMatch(direction: number) {
    if (!matches.length) return
    setMatchIndex(
      (index) => (index + direction + matches.length) % matches.length
    )
    resetView()
  }

  return (
    <>
      <div className='flex min-w-0 flex-wrap items-center gap-2 border-b p-2'>
        <Button
          size='xs'
          type='button'
          variant='ghost'
          onClick={() => {
            resetView()
            setMode(true)
          }}
        >
          {t('Expand all')}
        </Button>
        <Button
          size='xs'
          type='button'
          variant='ghost'
          onClick={() => {
            resetView()
            setMode(false)
          }}
        >
          {t('Collapse all')}
        </Button>
        <Input
          type='search'
          aria-label={t('Search JSON')}
          placeholder={t('Search JSON')}
          value={query}
          className='min-w-32 flex-1 basis-40'
          onChange={(event) => {
            setQuery(event.target.value)
            setMatchIndex(0)
            resetView()
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              navigateMatch(event.shiftKey ? -1 : 1)
            }
          }}
        />
        {query && (
          <span
            role='status'
            aria-live='polite'
            className='text-muted-foreground text-xs'
          >
            {matches.length
              ? t('{{current}} of {{total}} matches', {
                  current: formatNumber(matchIndex + 1, locale),
                  total: formatNumber(matches.length, locale),
                })
              : t('No matches')}
          </span>
        )}
        <div className='flex items-center gap-1'>
          <Button
            aria-label={t('Previous match')}
            disabled={!matches.length}
            size='icon-xs'
            type='button'
            variant='ghost'
            onClick={() => navigateMatch(-1)}
          >
            <ChevronUp aria-hidden='true' />
          </Button>
          <Button
            aria-label={t('Next match')}
            disabled={!matches.length}
            size='icon-xs'
            type='button'
            variant='ghost'
            onClick={() => navigateMatch(1)}
          >
            <ChevronDown aria-hidden='true' />
          </Button>
        </div>
      </div>
      <div
        ref={viewportRef}
        className='code-block-scroll max-h-128 overflow-auto p-3 font-mono text-xs leading-6'
      >
        <ul aria-label={t('JSON tree')} className='m-0 list-none p-0'>
          {view.rows.map((row) => {
            const entry = row.entry
            if (row.kind === 'node') {
              return (
                <JsonTreeRow
                  key={entry.node.from}
                  code={props.code}
                  entry={entry}
                  expanded={Boolean(row.expanded)}
                  match={match?.entry === entry ? match : undefined}
                  rowRef={match?.entry === entry ? activeRef : undefined}
                  onToggle={() =>
                    setExpanded((previous) =>
                      new Map(previous).set(entry.node.from, !row.expanded)
                    )
                  }
                />
              )
            }
            if (row.kind === 'closing') {
              return (
                <li
                  key={`end-${entry.node.from}`}
                  aria-hidden='true'
                  className='text-muted-foreground py-0.5'
                  style={{
                    paddingLeft: `min(${entry.depth * 1.25 + 1.75}rem, 25%)`,
                  }}
                >
                  {entry.node.name === 'ArrayExpression' ? ']' : '}'}
                </li>
              )
            }
            const page = row.page
            if (!page) return null
            return (
              <li
                key={`${row.kind}-${entry.node.from}`}
                style={{
                  paddingLeft: `min(${entry.depth * 1.25 + 1.75}rem, 25%)`,
                }}
              >
                <Button
                  className='my-1'
                  size='xs'
                  type='button'
                  variant='ghost'
                  onClick={() =>
                    setPages((previous) => {
                      const next =
                        row.kind === 'previous'
                          ? {
                              start: Math.max(0, page.start - CHILD_BATCH_SIZE),
                              count: CHILD_BATCH_SIZE,
                            }
                          : { ...page, count: page.count + CHILD_BATCH_SIZE }
                      return new Map(previous).set(entry.node.from, next)
                    })
                  }
                >
                  {row.kind === 'previous'
                    ? t('Show previous items')
                    : t('Show more ({{remaining}} remaining)', {
                        remaining: formatNumber(
                          entry.children.length - page.start - page.count,
                          locale
                        ),
                      })}
                </Button>
              </li>
            )
          })}
        </ul>
        {view.hasMore && (
          <Button
            className='mt-2'
            size='xs'
            type='button'
            variant='ghost'
            onClick={() => setLimit((value) => value + NODE_BATCH_SIZE)}
          >
            {t('Show more nodes')}
          </Button>
        )}
      </div>
    </>
  )
}
