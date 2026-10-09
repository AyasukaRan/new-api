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
import { getJsonChildren, type JsonSyntaxNode } from './json-source'

export const CHILD_BATCH_SIZE = 50
export const NODE_BATCH_SIZE = 200

export type JsonEntry = {
  node: JsonSyntaxNode
  label?: string
  name: string
  parent: JsonEntry | null
  position: number
  depth: number
  children: JsonEntry[]
}

export type JsonPage = { start: number; count: number }
export type JsonMatch = { entry: JsonEntry; key: boolean; value: boolean }
export type JsonRow = {
  entry: JsonEntry
  kind: 'node' | 'closing' | 'previous' | 'more'
  expanded?: boolean
  page?: JsonPage
}

/** Keep source nodes, not parsed values: duplicate properties remain distinct. */
export function indexJsonTree(root: JsonSyntaxNode, code: string): JsonEntry[] {
  const entries: JsonEntry[] = []
  const pending: JsonEntry[] = [
    {
      node: root,
      name: '$',
      parent: null,
      position: 0,
      depth: 0,
      children: [],
    },
  ]
  while (pending.length) {
    const entry = pending.pop()
    if (!entry) break
    entries.push(entry)
    entry.children = getJsonChildren(entry.node, code).map(
      (child, position) => ({
        ...child,
        parent: entry,
        position,
        depth: entry.depth + 1,
        children: [],
      })
    )
    for (let i = entry.children.length - 1; i >= 0; i--) {
      pending.push(entry.children[i])
    }
  }
  return entries
}

export function searchJsonTree(
  entries: JsonEntry[],
  code: string,
  query: string
): JsonMatch[] {
  if (!query) return []
  const search = query.toLowerCase()
  const matches: JsonMatch[] = []
  for (const entry of entries) {
    const key =
      entry.label !== undefined &&
      (entry.name.toLowerCase().includes(search) ||
        entry.label.toLowerCase().includes(search))
    let value = false
    if (!entry.children.length) {
      const source = code.slice(entry.node.from, entry.node.to)
      value = source.toLowerCase().includes(search)
      if (!value && entry.node.name === 'String') {
        value = (JSON.parse(source) as string).toLowerCase().includes(search)
      }
    }
    if (key || value) matches.push({ entry, key, value })
  }
  return matches
}

export function getJsonPath(entry: JsonEntry): string {
  const segments: string[] = []
  for (let current = entry; current.parent; current = current.parent) {
    segments.push(
      current.parent.node.name === 'ArrayExpression'
        ? `[${current.position}]`
        : `[${JSON.stringify(current.name)}]`
    )
  }
  return `$${segments.reverse().join('')}`
}

/** Derive a bounded flat view; rendering never mutates a shared row budget. */
export function getVisibleJsonRows(options: {
  root: JsonEntry
  expanded: Map<number, boolean>
  mode: boolean | undefined
  pages: Map<number, JsonPage>
  active: JsonEntry | undefined
  limit: number
}): { rows: JsonRow[]; hasMore: boolean } {
  const required = new Set<number>()
  for (let entry = options.active; entry; entry = entry.parent ?? undefined) {
    required.add(entry.node.from)
  }
  const rows: JsonRow[] = []
  const pending: JsonRow[] = [{ kind: 'node', entry: options.root }]
  let remaining = Math.max(0, options.limit - required.size)
  let hasMore = false
  while (pending.length) {
    const row = pending.pop()
    if (!row) break
    if (row.kind !== 'node') {
      rows.push(row)
      continue
    }
    const entry = row.entry
    if (!required.has(entry.node.from)) {
      if (!remaining) {
        hasMore = true
        continue
      }
      remaining--
    }
    const defaultExpanded = options.active
      ? required.has(entry.node.from) && entry !== options.active
      : entry.depth < 2
    const expanded =
      options.expanded.get(entry.node.from) ?? options.mode ?? defaultExpanded
    rows.push({ ...row, expanded })
    if (!expanded || !entry.children.length) continue

    let start = 0
    if (options.active) {
      const child = entry.children.find((item) => required.has(item.node.from))
      if (child) {
        start = Math.floor(child.position / CHILD_BATCH_SIZE) * CHILD_BATCH_SIZE
      }
    }
    const page = options.pages.get(entry.node.from) ?? {
      start,
      count: CHILD_BATCH_SIZE,
    }
    const end = Math.min(page.start + page.count, entry.children.length)
    pending.push({ kind: 'closing', entry })
    if (end < entry.children.length) pending.push({ kind: 'more', entry, page })
    for (let i = end - 1; i >= page.start; i--) {
      pending.push({ kind: 'node', entry: entry.children[i] })
    }
    if (page.start > 0) pending.push({ kind: 'previous', entry, page })
  }
  return { rows, hasMore }
}
