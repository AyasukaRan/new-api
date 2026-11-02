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
import { expect, it, vi } from 'vitest'

import { ModelDetailsApi } from '../components/model-details-api'
import type { PricingModel } from '../types'

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({
    status: { server_address: 'https://gateway.example/new-api' },
  }),
}))
vi.mock('@/components/ai-elements/code-block', () => ({
  CodeBlock: ({ code }: { code: string }) => (
    <pre data-testid='sample'>{code}</pre>
  ),
  CodeBlockCopyButton: () => null,
}))

it.each(['search', 'fetch'])(
  'gives working request examples and parameters for DeepSeek %s',
  async (tool) => {
    const type = 'deepseek-tools-' + tool
    const path = '/v1/tools/' + tool
    const key = tool === 'search' ? 'query' : 'url'
    const model: PricingModel = {
      id: 1,
      model_name: 'deepseek-' + tool,
      model_ratio: 0,
      completion_ratio: 0,
      quota_type: 1,
      enable_groups: ['default'],
      supported_endpoint_types: [type],
    }
    render(
      <ModelDetailsApi
        model={model}
        endpointMap={{ [type]: { path, method: 'POST' } }}
      />
    )
    const user = userEvent.setup()
    for (const lang of ['cURL', 'Python', 'TypeScript', 'JavaScript']) {
      await user.click(screen.getByRole('tab', { name: lang }))
      const example = screen.getByTestId('sample').textContent ?? ''
      expect(example).toContain('https://gateway.example/new-api' + path)
      expect(example).toContain('"' + key + '"')
      expect(example).toContain('NEW_API_KEY')
      expect(example).not.toMatch(/chat\.completions|messages|temperature/)
    }
    expect(screen.getByText(key, { exact: true })).toBeInTheDocument()
  }
)
