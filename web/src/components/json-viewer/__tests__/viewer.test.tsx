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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test } from 'vitest'

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
})
