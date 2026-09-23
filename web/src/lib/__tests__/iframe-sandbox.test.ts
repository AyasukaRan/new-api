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
import { describe, expect, it } from 'vitest'

import { iframeSandbox } from '../iframe-sandbox'

describe('iframe sandbox', () => {
  it.each([
    undefined,
    '/preview',
    'https://platform.example/preview',
    'data:text/html,test',
    'blob:https://platform.example/id',
    'javascript:alert(1)',
    'https://[',
  ])('isolates local or opaque content %s from platform credentials', (src) => {
    const policy = iframeSandbox(src, 'https://platform.example').split(' ')
    expect(policy).toContain('allow-scripts')
    expect(policy).not.toContain('allow-same-origin')
    expect(policy).not.toContain('allow-top-navigation')
  })
  it('lets an external preview keep its own login and storage without navigating the parent', () => {
    const policy = iframeSandbox(
      'https://chat.example/session',
      'https://platform.example'
    ).split(' ')
    expect(policy).toContain('allow-same-origin')
    expect(policy).toContain('allow-forms')
    expect(policy).not.toContain('allow-top-navigation')
  })
  it.each([
    ['', ''],
    ['allow-forms', 'allow-forms'],
    [
      'allow-scripts allow-same-origin allow-top-navigation',
      'allow-scripts allow-same-origin',
    ],
    ['  ALLOW-FORMS  allow-forms  ', 'allow-forms'],
  ])(
    'preserves the stricter caller policy %s for external previews',
    (requested, expected) => {
      expect(
        iframeSandbox(
          'https://preview.example/page',
          'https://platform.example',
          requested
        )
      ).toBe(expected)
    }
  )
  it('does not let a requested policy restore same-origin access for inline or local previews', () => {
    const requested = 'allow-scripts allow-same-origin allow-top-navigation'
    expect(
      iframeSandbox(undefined, 'https://platform.example', requested)
    ).toBe('allow-scripts')
    expect(
      iframeSandbox('/preview', 'https://platform.example', requested)
    ).toBe('allow-scripts')
  })
})
