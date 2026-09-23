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
import { describe, expect, test } from 'vitest'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '../tabs'

describe('tabs layout and nested navigation', () => {
  test.each([
    'h-auto',
    'group-data-horizontal/tabs:h-auto',
    'data-horizontal:h-auto',
  ])(
    '%s allows a horizontal tab list to grow for wrapped content',
    (heightOverride) => {
      render(
        <Tabs defaultValue='first'>
          <TabsList
            aria-label='Views'
            className={`flex-wrap ${heightOverride}`}
          >
            <TabsTrigger value='first'>First view</TabsTrigger>
            <TabsTrigger value='second'>Second view</TabsTrigger>
          </TabsList>
        </Tabs>
      )

      const list = screen.getByRole('tablist', { name: 'Views' })
      expect(list).toHaveAttribute('data-orientation', 'horizontal')
      expect(list).toHaveClass('flex-wrap', heightOverride)
      // An orientation-scoped fixed height wins over existing auto-height
      // overrides in Tailwind's generated cascade and clips wrapped tabs.
      expect(list).not.toHaveClass('data-horizontal:h-8')
      if (heightOverride === 'h-auto') expect(list).not.toHaveClass('h-8')
    }
  )

  test('horizontal payload tabs keep their own layout and arrow keys inside vertical tabs', async () => {
    render(
      <Tabs defaultValue='request' orientation='vertical'>
        <TabsList aria-label='Exchanges'>
          <TabsTrigger value='request'>Request</TabsTrigger>
          <TabsTrigger value='response'>Response</TabsTrigger>
        </TabsList>
        <TabsContent value='request'>
          <Tabs defaultValue='body'>
            <TabsList aria-label='Payload'>
              <TabsTrigger value='body'>Body</TabsTrigger>
              <TabsTrigger value='headers'>Headers</TabsTrigger>
            </TabsList>
            <TabsContent value='body'>Request body</TabsContent>
            <TabsContent value='headers'>Request headers</TabsContent>
          </Tabs>
        </TabsContent>
      </Tabs>
    )

    const exchanges = screen.getByRole('tablist', { name: 'Exchanges' })
    const payload = screen.getByRole('tablist', { name: 'Payload' })
    expect(exchanges).toHaveAttribute('data-orientation', 'vertical')
    expect(exchanges).toHaveClass('data-vertical:h-fit')
    expect(payload).toHaveAttribute('data-orientation', 'horizontal')
    expect(payload).toHaveClass('data-vertical:flex-col')
    expect(payload).not.toHaveClass('group-data-vertical/tabs:flex-col')

    within(payload).getByRole('tab', { name: 'Body' }).focus()
    await userEvent.keyboard('{ArrowRight}{Enter}')

    expect(
      within(payload).getByRole('tab', { name: 'Headers' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(
      within(exchanges).getByRole('tab', { name: 'Request' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('Request headers')).toBeInTheDocument()
  })
})
