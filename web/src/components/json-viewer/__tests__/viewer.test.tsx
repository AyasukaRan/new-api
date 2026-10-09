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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'

import { JsonViewer } from '../../json-viewer'

describe('JsonViewer', () => {
  test('expands nested JSON by keyboard and keeps exact numeric values and duplicate keys', async () => {
    const user = userEvent.setup()
    render(
      <JsonViewer code='{"rows":[{"id":9007199254740993,"rate":0.1234567890123456789,"same":1,"same":2,"ok":false,"empty":null}]}' />
    )

    const row = screen.getByRole('button', { name: 'Expand 0' })
    expect(row).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('9007199254740993')).not.toBeInTheDocument()
    row.focus()
    await user.keyboard('{Enter}')
    expect(screen.getByRole('button', { name: 'Collapse 0' })).toHaveAttribute(
      'aria-expanded',
      'true'
    )
    expect(screen.getByText('9007199254740993')).toBeVisible()
    expect(screen.getByText('0.1234567890123456789')).toBeVisible()
    expect(screen.getAllByText('"same":')).toHaveLength(2)
    expect(screen.getByText('false')).toBeVisible()
    expect(screen.getByText('null')).toBeVisible()
  })

  test('copies the untouched original source and can switch between raw and structured views', async () => {
    const user = userEvent.setup()
    const code = '  { "value": 9007199254740993, "text": "a\\nb" }\n'
    render(<JsonViewer code={code} />)

    await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
    expect(await navigator.clipboard.readText()).toBe(code)
    await user.click(screen.getByRole('button', { name: 'Raw JSON' }))
    expect(screen.getByRole('textbox', { name: 'JSON' })).toHaveTextContent(
      '9007199254740993'
    )
    await user.click(screen.getByRole('button', { name: 'JSON tree' }))
    expect(screen.getByRole('button', { name: 'Collapse $' })).toBeVisible()
  })

  test('keeps truncated JSON intact as a readable raw payload', () => {
    const code = '{"rows":[{"id":9007199254740993}, [truncated]'
    render(<JsonViewer code={code} title='Body' />)

    expect(screen.getByRole('textbox', { name: 'Body' })).toHaveTextContent(
      code
    )
    expect(
      screen.queryByRole('button', { name: 'Collapse $' })
    ).not.toBeInTheDocument()
  })

  test('renders a bounded set of array entries and reveals remaining entries on demand', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 51 }, (_, index) => `row-${index}`)
    render(<JsonViewer code={JSON.stringify(rows)} />)

    expect(screen.queryByText('"row-50"')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Show more/ }))
    expect(screen.getByText('"row-50"')).toBeVisible()
  })

  test('expands long string values on demand without clipping the copied source', async () => {
    const user = userEvent.setup()
    const value = `${'A report with detailed observations. '.repeat(20)}Final detail`
    const code = JSON.stringify({ report: value })
    render(<JsonViewer code={code} />)

    expect(screen.queryByText(JSON.stringify(value))).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show full value' }))
    expect(screen.getByText(JSON.stringify(value))).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
    expect(await navigator.clipboard.readText()).toBe(code)
  })

  test('recognizes JSON surrounded by a byte order mark while copying the untouched source', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    const code = '\ufeff {"rows":[{"id":9007199254740993}]} \ufeff'
    render(<JsonViewer code={code} />)

    expect(screen.getByRole('button', { name: 'Collapse rows' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Expand 0' }))
    expect(screen.getByText('9007199254740993')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
    expect(writeText).toHaveBeenCalledExactlyOnceWith(code)
  })

  test('keeps the original payload when valid JSON cannot form a reliable syntax tree', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    // JSON permits lone UTF-16 surrogates, but the JavaScript syntax parser
    // inserts recovery nodes for the unescaped surrogate in this source.
    const code = '{"value":"\ud800"}'
    render(<JsonViewer code={code} title='Body' />)

    expect(screen.getByRole('textbox', { name: 'Body' })).toHaveTextContent(
      code
    )
    expect(
      screen.queryByRole('button', { name: 'Collapse $' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Copy JSON' }))
    expect(writeText).toHaveBeenCalledExactlyOnceWith(code)
  })

  test('shows empty containers, null and false without hiding them', () => {
    render(
      <JsonViewer code='{"object":{},"array":[],"nothing":null,"no":false,"zero":0}' />
    )

    expect(screen.getByText('{}')).toBeVisible()
    expect(screen.getByText('[]')).toBeVisible()
    expect(screen.getByText('null')).toBeVisible()
    expect(screen.getByText('false')).toBeVisible()
    expect(screen.getByText('0')).toBeVisible()
  })

  test('keeps a short key in the value text flow and wraps long words without break-all', () => {
    render(
      <JsonViewer code='{"text":"Ord med mellemrum og etmegetlangtordudenmellemrum"}' />
    )

    const key = screen.getByText('"text":')
    const content = key.parentElement
    expect(content).toHaveClass('wrap-anywhere', 'whitespace-pre-wrap')
    expect(content).not.toHaveClass('flex', 'break-all')
    expect(content).toContainElement(
      screen.getByText('"Ord med mellemrum og etmegetlangtordudenmellemrum"')
    )
  })

  test('expands and collapses all containers while keeping large documents incrementally visible', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 50 }, (_, index) => ({
      details: { id: index, enabled: true, label: `entry-${index}` },
    }))
    render(<JsonViewer code={JSON.stringify({ rows })} />)

    await user.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(screen.getByText('"entry-0"')).toBeVisible()
    expect(screen.queryByText('"entry-49"')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show more nodes' }))
    expect(screen.getByText('"entry-49"')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Collapse all' }))
    expect(screen.getByRole('button', { name: 'Expand $' })).toHaveAttribute(
      'aria-expanded',
      'false'
    )
    expect(screen.queryByText('"entry-0"')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(screen.getByText('"entry-0"')).toBeVisible()
  })

  test('finds keys and values in collapsed and paginated nodes, navigates hits and clears the search', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 52 }, (_, index) => ({
      label: `row-${index}`,
    }))
    rows[1] = { label: 'needle value' }
    const code = JSON.stringify({ rows }).replace(
      '"label":"row-51"',
      '"needle_key":"last row"'
    )
    render(<JsonViewer code={code} />)
    await user.click(screen.getByRole('button', { name: 'Collapse all' }))

    const search = screen.getByRole('searchbox', { name: 'Search JSON' })
    await user.type(search, 'needle')
    expect(screen.getByRole('status')).toHaveTextContent('1 of 2 matches')
    const first = screen.getByText('"needle value"')
    expect(first.closest('mark')).toBeInTheDocument()
    expect(first.closest('li')).toHaveAttribute('aria-current', 'true')
    await user.click(screen.getByRole('button', { name: 'Next match' }))
    const last = screen.getByText('"needle_key":')
    expect(last.closest('mark')).toBeInTheDocument()
    expect(last.closest('li')).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('status')).toHaveTextContent('2 of 2 matches')
    expect(screen.queryByText('"row-0"')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Previous match' }))
    expect(screen.getByText('"needle value"').closest('li')).toHaveAttribute(
      'aria-current',
      'true'
    )
    await user.clear(search)
    expect(screen.getByRole('button', { name: 'Expand 1' })).toBeVisible()
    expect(screen.queryByText('"needle value"')).not.toBeInTheDocument()
    await user.type(search, 'absent')
    expect(screen.getByRole('status')).toHaveTextContent('No matches')
    expect(screen.getByRole('button', { name: 'Next match' })).toBeDisabled()
  })

  test('copies each duplicate node verbatim and copies escaped JSONPath segments', async () => {
    const user = userEvent.setup()
    const code =
      '{"a.b":[{"id":9007199254740993,"id":0.1234567890123456789}],"a\\\"b":false}'
    render(<JsonViewer code={code} />)
    await user.click(screen.getByRole('button', { name: 'Expand 0' }))

    const bigInteger = screen.getByText('9007199254740993').closest('li')
    expect(bigInteger).not.toBeNull()
    if (!bigInteger) throw new Error('Missing integer node')
    await user.click(
      within(bigInteger).getByRole('button', { name: 'Copy node JSON' })
    )
    expect(await navigator.clipboard.readText()).toBe('9007199254740993')
    await user.click(
      within(bigInteger).getByRole('button', { name: 'Copy JSON path' })
    )
    expect(await navigator.clipboard.readText()).toBe('$["a.b"][0]["id"]')
    const decimal = screen.getByText('0.1234567890123456789').closest('li')
    expect(decimal).not.toBeNull()
    if (!decimal) throw new Error('Missing decimal node')
    await user.click(
      within(decimal).getByRole('button', { name: 'Copy node JSON' })
    )
    expect(await navigator.clipboard.readText()).toBe('0.1234567890123456789')
    const escapedKey = screen.getByText('false').closest('li')
    expect(escapedKey).not.toBeNull()
    if (!escapedKey) throw new Error('Missing escaped-key node')
    await user.click(
      within(escapedKey).getByRole('button', { name: 'Copy JSON path' })
    )
    expect(await navigator.clipboard.readText()).toBe('$["a\\\"b"]')
  })

  test('aligns search results to the JSON viewport top when an ancestor clips its lower half', async () => {
    const user = userEvent.setup()
    render(<JsonViewer code='{"rows":[{"text":"needle"}]}' />)
    const viewport = screen.getByRole('list', {
      name: 'JSON tree',
    }).parentElement
    if (!viewport) throw new Error('Missing JSON viewport')
    const scrollIntoView = vi.spyOn(HTMLElement.prototype, 'scrollIntoView')
    const originalBounds = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
      function (this: HTMLElement) {
        if (this === viewport) return new DOMRect(0, 100, 800, 200)
        if (this.getAttribute('aria-current') === 'true') {
          return new DOMRect(0, 500 - viewport.scrollTop, 800, 24)
        }
        return originalBounds.call(this)
      }
    )

    await user.type(
      screen.getByRole('searchbox', { name: 'Search JSON' }),
      'needle'
    )
    expect(screen.getByText('"needle"')).toBeVisible()
    // An enclosing conversation may clip the viewport's lower half, so
    // aligning with its bottom can still leave the result out of sight.
    expect(viewport.scrollTop).toBe(400)
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  test('resets search and disclosure state when a different payload is displayed', async () => {
    const user = userEvent.setup()
    const view = render(<JsonViewer code='{"rows":[{"id":"old result"}]}' />)
    await user.type(
      screen.getByRole('searchbox', { name: 'Search JSON' }),
      'old result'
    )
    expect(screen.getByText('"old result"')).toBeVisible()

    view.rerender(<JsonViewer code='{"rows":[{"id":"new result"}]}' />)
    expect(screen.getByRole('searchbox', { name: 'Search JSON' })).toHaveValue(
      ''
    )
    expect(screen.getByRole('button', { name: 'Expand 0' })).toBeVisible()
    expect(screen.queryByText('"new result"')).not.toBeInTheDocument()
  })
})
