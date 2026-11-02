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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import type { ModelPricingEntry } from '@/features/model-pricing/api'
import type { BillingUsageSchema } from '@/features/pricing/types'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  ModelRatioVisualEditor,
  type ModelRatioVisualEditorHandle,
} from '../model-ratio-visual-editor'
import type { ModelPricingIssue } from '../pricing-issues'

let client: QueryClient | undefined

afterEach(() => {
  client?.clear()
  localStorage.clear()
  window.getSelection()?.removeAllRanges()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

type EditorFixture = {
  issues?: ModelPricingIssue[]
  filterMode?: 'unset'
  removedChannelId?: number
  entry?: Partial<ModelPricingEntry>
  channelPrices?: Record<string, unknown>
  disabledChannelId?: number
  taskModels?: string[]
  billingMode?: string
  billingExpr?: string
  pluginBillingExpr?: string
}

async function renderEditor(fixture: EditorFixture = {}) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing') {
      const data = (fixture.taskModels ?? []).map((name) => ({
        model_name: name,
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      }))
      return { data: { success: true, data, vendors: [] } }
    }
    if (url === '/api/option/model_pricing/issues') {
      return { data: { success: true, data: fixture.issues ?? [] } }
    }
    if (url === '/api/option/model_pricing') {
      return {
        data: {
          success: true,
          data: {
            entries: [
              {
                model_name: 'gpt-4.1-mini',
                effective: {
                  'billing_setting.billing_mode': 'tiered_expr',
                  'billing_setting.billing_expr': 'tier("base", p * 1 + c * 2)',
                },
                ...fixture.entry,
              },
            ],
          },
        },
      }
    }
    if (url === '/api/option/') {
      return {
        data: {
          success: true,
          data: [
            {
              key: 'ChannelModelPricing',
              value: JSON.stringify(fixture.channelPrices ?? {}),
            },
          ],
        },
      }
    }
    if (url === '/api/channel') {
      const items = (fixture.issues ?? [])
        .filter(
          (issue) =>
            issue.channel_id && issue.channel_id !== fixture.removedChannelId
        )
        .map((issue) => ({
          id: issue.channel_id,
          name: issue.channel_name,
          models: issue.model_name,
          status: issue.channel_id === fixture.disabledChannelId ? 2 : 1,
        }))
      return { data: { success: true, data: { items, total: items.length } } }
    }
    return { data: { success: true, data: {} } }
  })
  if (fixture.issues) {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'root', role: ROLE.SUPER_ADMIN })
  }
  const modelRatio = JSON.stringify({
    'gpt-4.1-mini': 0.2,
    'claude-sonnet': 1.5,
  })
  const billingMode = fixture.billingMode ?? '{}'
  const billingExpr = fixture.billingExpr ?? '{}'
  const pluginBillingExpr = fixture.pluginBillingExpr ?? '{}'
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client = queryClient
  const editor = createRef<ModelRatioVisualEditorHandle>()
  const onChange = vi.fn()
  const view = render(
    <QueryClientProvider client={client}>
      <ModelRatioVisualEditor
        ref={editor}
        savedModelPrice='{}'
        savedModelRatio={modelRatio}
        savedCacheRatio='{}'
        savedCreateCacheRatio='{}'
        savedCompletionRatio='{}'
        savedImageRatio='{}'
        savedAudioRatio='{}'
        savedAudioCompletionRatio='{}'
        savedBillingMode={billingMode}
        savedBillingExpr={billingExpr}
        savedPluginBillingExpr={pluginBillingExpr}
        modelPrice='{}'
        modelRatio={modelRatio}
        cacheRatio='{}'
        createCacheRatio='{}'
        completionRatio='{}'
        imageRatio='{}'
        audioRatio='{}'
        audioCompletionRatio='{}'
        billingMode={billingMode}
        billingExpr={billingExpr}
        pluginBillingExpr={pluginBillingExpr}
        onChange={onChange}
        onSave={async () => {
          await editor.current?.commitOpenEditor()
        }}
        filterMode={fixture.filterMode}
        isSaving={false}
      />
    </QueryClientProvider>
  )
  // The real page has pricing metadata loaded long before the user drags over
  // a model name, so settle the pricing query before interacting.
  await waitFor(() => expect(queryClient.isFetching()).toBe(0))
  return { ...view, onChange }
}

// A drag-select ends with mousedown/mouseup inside the row and the browser
// then fires `click` on the row. Only that final `click` is dispatched here so
// the text selection created by the drag is still active when the row opens
// the editor.
it('keeps the drag-selected model name highlighted when the row click opens the editor', async () => {
  await renderEditor()
  const row = await screen.findByRole('row', { name: /gpt-4\.1-mini/ })
  const nameCell = within(row).getByText('gpt-4.1-mini')
  window.getSelection()?.selectAllChildren(nameCell)
  expect(String(window.getSelection())).toBe('gpt-4.1-mini')

  fireEvent.click(nameCell)

  expect(
    await screen.findByRole('region', { name: 'Edit model pricing' })
  ).toBeInTheDocument()
  expect(nameCell).toBeInTheDocument()
  expect(String(window.getSelection())).toBe('gpt-4.1-mini')
})

it('keeps other rows mounted when a different model is opened for editing', async () => {
  await renderEditor()
  const targetRow = await screen.findByRole('row', { name: /gpt-4\.1-mini/ })
  const otherName = within(
    screen.getByRole('row', { name: /claude-sonnet/ })
  ).getByText('claude-sonnet')

  fireEvent.click(within(targetRow).getByText('gpt-4.1-mini'))

  expect(
    await screen.findByRole('region', { name: 'Edit model pricing' })
  ).toBeInTheDocument()
  expect(otherName).toBeInTheDocument()
})

// kling-v3 was never priced, issue-shared's model expression reads usage its
// new provider lacks, and vidu1.5 keeps a provider price its plugin dropped.
const pricingIssueFixture: EditorFixture = {
  taskModels: ['issue-shared'],
  billingMode: JSON.stringify({ 'issue-shared': 'tiered_expr' }),
  billingExpr: JSON.stringify({ 'issue-shared': 'u("seconds") * 0.2' }),
  pluginBillingExpr: JSON.stringify({ 'vidu::vidu1.5': 'u("seconds") * 0.1' }),
  issues: [
    {
      model_name: 'issue-shared',
      plugin_key: 'beta',
      plugin_name: 'Beta',
      reason: 'incompatible',
    },
    {
      model_name: 'kling-v3',
      plugin_key: 'kling',
      plugin_name: 'Kling',
      reason: 'unconfigured',
    },
    {
      model_name: 'vidu1.5',
      plugin_key: 'vidu',
      plugin_name: 'Vidu',
      reason: 'stale',
    },
  ],
}

it.each([
  ['issue-shared', 'Beta: The price uses usage the plugin no longer provides'],
  ['kling-v3', 'Kling: No price set'],
  ['vidu1.5', 'Vidu: The plugin no longer offers this model'],
])(
  'marks %s as needing price attention with the reason instead of a configured price',
  async (name, reason) => {
    await renderEditor(pricingIssueFixture)
    const row = await screen.findByRole('row', {
      name: new RegExp(name.replace('.', '\\.')),
    })

    expect(within(row).getByText('Price needs attention')).toBeVisible()
    expect(within(row).getByText(reason)).toBeVisible()
    expect(within(row).queryByText('Task pricing')).not.toBeInTheDocument()
  }
)

it('keeps the configured badges on models without pricing issues', async () => {
  await renderEditor({ ...pricingIssueFixture, issues: [] })
  const row = await screen.findByRole('row', { name: /issue-shared/ })

  expect(within(row).getByText('Task pricing')).toBeVisible()
  expect(
    within(row).queryByText('Price needs attention')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('row', { name: /kling-v3/ })
  ).not.toBeInTheDocument()
})

it('filters the table down to the models whose price needs attention', async () => {
  const user = userEvent.setup()
  await renderEditor(pricingIssueFixture)
  await screen.findByRole('row', { name: /kling-v3/ })
  const filter = screen.getByRole('button', { name: /Price status/ })

  await user.click(filter)
  await user.click(
    await screen.findByRole('option', { name: /Price needs attention/ })
  )

  await waitFor(() =>
    expect(
      screen.queryByRole('row', { name: /gpt-4\.1-mini/ })
    ).not.toBeInTheDocument()
  )
  for (const name of [/issue-shared/, /kling-v3/, /vidu1\.5/]) {
    expect(screen.getByRole('row', { name })).toBeInTheDocument()
  }
  expect(
    screen.queryByRole('row', { name: /claude-sonnet/ })
  ).not.toBeInTheDocument()
  expect(within(filter).getByText('Price needs attention')).toBeInTheDocument()
})

it('lists one row per affected model and opens each affected channel without touching global pricing', async () => {
  const user = userEvent.setup()
  const shared = 'gpt-4.1-mini'
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const patch = vi.spyOn(api, 'patch')
  await renderEditor({
    filterMode: 'unset',
    issues: [
      {
        model_name: shared,
        channel_id: 7,
        channel_name: 'Ordinary',
        reason: 'invalid_expression',
      },
      {
        model_name: shared,
        channel_id: 12,
        channel_name: 'Task route',
        plugin_key: 'beta',
        plugin_name: 'Beta',
        reason: 'invalid_task_price',
      },
    ],
  })

  const rows = screen.getAllByRole('row', { name: /gpt-4\.1-mini/ })
  expect(rows).toHaveLength(1)
  expect(
    screen.queryByRole('row', { name: /claude-sonnet/ })
  ).not.toBeInTheDocument()
  await user.click(
    within(rows[0]).getByRole('button', {
      name: '#12 Task route · Beta: Task pricing is required for this provider',
    })
  )
  await waitFor(() =>
    expect(screen.getByRole('combobox', { name: 'Pricing scope' })).toHaveValue(
      '#12 Task route · Enabled'
    )
  )
  await user.click(
    within(rows[0]).getByRole('button', {
      name: '#7 Ordinary: Invalid billing expression',
    })
  )
  await waitFor(() =>
    expect(screen.getByRole('combobox', { name: 'Pricing scope' })).toHaveValue(
      '#7 Ordinary · Enabled'
    )
  )
  await user.click(
    await screen.findByRole('button', { name: 'Save channel pricing' })
  )
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  const written = put.mock.calls[0][1] as { key: string; value: string }
  expect(written.key).toBe('ChannelModelPricing')
  expect(Object.keys(JSON.parse(written.value)[shared])).toEqual(['7'])
  expect(patch).not.toHaveBeenCalled()
})

it.each(['removed', 'disabled'])(
  'blocks a stale alert for a %s channel even if it retains a saved price',
  async (state) => {
    await renderEditor({
      filterMode: 'unset',
      removedChannelId: state === 'removed' ? 7 : undefined,
      disabledChannelId: state === 'disabled' ? 7 : undefined,
      channelPrices: {
        'gpt-4.1-mini': {
          '7': { billing_mode: 'per_request', model_price: 1 },
        },
      },
      issues: [
        {
          model_name: 'gpt-4.1-mini',
          channel_id: 7,
          channel_name: 'Removed',
          reason: 'unconfigured',
        },
      ],
    })
    fireEvent.click(
      await screen.findByRole('button', { name: '#7 Removed: No price set' })
    )
    expect(await screen.findByText('Unavailable channel')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Save channel pricing' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Save model prices' })
    ).not.toBeInTheDocument()
  }
)

const sharedModel = 'gpt-4.1-mini'
const alphaSchema: BillingUsageSchema = {
  seconds: {
    type: 'number',
    unit: 'second',
    description: { en: 'Alpha duration price' },
  },
}
const betaSchema: BillingUsageSchema = {
  clips: {
    type: 'number',
    unit: 'count',
    description: { en: 'Beta clip price' },
  },
}
const providers = [
  {
    plugin_key: 'alpha',
    plugin_name: 'Alpha',
    usage_schema: alphaSchema,
    configured: '',
    effective: '',
    compatible: false,
  },
  {
    plugin_key: 'beta',
    plugin_name: 'Beta',
    usage_schema: betaSchema,
    configured: '',
    effective: '',
    compatible: false,
  },
]
const betaIssue: ModelPricingIssue = {
  model_name: sharedModel,
  channel_id: 12,
  channel_name: 'Beta channel',
  plugin_key: 'beta',
  plugin_name: 'Beta',
  usage_schema: betaSchema,
  reason: 'unconfigured',
}
it('uses the executing plugin schema for a channel price and ordinary token fields for its namesake', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  await renderEditor({
    filterMode: 'unset',
    taskModels: [sharedModel],
    entry: {
      effective: {},
      usage_schema: alphaSchema,
      plugin_variants: providers,
    },
    issues: [
      betaIssue,
      {
        model_name: sharedModel,
        channel_id: 7,
        channel_name: 'Ordinary',
        reason: 'unconfigured',
      },
    ],
  })
  await user.click(
    screen.getByRole('button', {
      name: '#12 Beta channel · Beta: No price set',
    })
  )
  expect((await screen.findAllByText('Beta clip price'))[0]).toBeVisible()
  expect(screen.queryByText('Alpha duration price')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save channel pricing' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  expect(
    JSON.parse((put.mock.calls[0][1] as { value: string }).value)[sharedModel][
      '12'
    ].billing_expr
  ).toContain('u("clips")')
  await user.click(
    screen.getByRole('button', { name: '#7 Ordinary: No price set' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'Save channel pricing' })
  )
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(
    JSON.parse((put.mock.calls[1][1] as { value: string }).value)[sharedModel][
      '7'
    ].billing_expr
  ).not.toContain('u(')
})
it.each(['direct', 'mapped', 'inherit'] as const)(
  'keeps plugin prices separate when the channel override is %s',
  async (mode) => {
    const hasOverride = mode !== 'inherit'
    const overrideModel = mode === 'mapped' ? 'upstream-model' : sharedModel
    const user = userEvent.setup()
    const put = vi.spyOn(api, 'put')
    const { onChange } = await renderEditor({
      filterMode: 'unset',
      entry: {
        effective: {},
        usage_schema: alphaSchema,
        plugin_variants: providers,
      },
      issues: [
        {
          ...betaIssue,
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          usage_schema: alphaSchema,
          channel_shared_by_plugins: true,
          channel_billing_override_model: hasOverride
            ? overrideModel
            : undefined,
        },
        {
          ...betaIssue,
          channel_shared_by_plugins: true,
          channel_billing_override_model: hasOverride
            ? overrideModel
            : undefined,
        },
      ],
      pluginBillingExpr: JSON.stringify({
        'alpha::gpt-4.1-mini': 'u("seconds") * 2',
      }),
      channelPrices: hasOverride
        ? {
            [overrideModel]: {
              '12': {
                billing_mode: 'tiered_expr',
                billing_expr: 'u("seconds")',
              },
            },
          }
        : {},
    })
    await user.click(
      screen.getByRole('button', {
        name: '#12 Beta channel · Beta: No price set',
      })
    )
    await user.click(
      screen.getByRole('button', {
        name: '#12 Beta channel · Alpha: No price set',
      })
    )
    expect(
      await screen.findByRole('button', {
        name: 'Edit global pricing for Alpha',
      })
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', {
        name: '#12 Beta channel · Beta: No price set',
      })
    )
    const open = await screen.findByRole('button', {
      name: 'Edit global pricing for Beta',
    })
    expect(
      screen.queryByRole('button', { name: 'Save channel pricing' })
    ).not.toBeInTheDocument()
    if (hasOverride) {
      expect(open).toBeDisabled()
      expect(
        screen.getByText(overrideModel, { selector: 'code' })
      ).toBeVisible()
      if (mode === 'direct') {
        expect(
          screen.getByRole('button', { name: 'Use global pricing' })
        ).toBeVisible()
      }
    } else {
      await user.click(open)
      expect(
        await screen.findByRole('tab', { name: 'Beta', selected: true })
      ).toBeVisible()
      await user.click(
        screen.getByRole('switch', { name: /Set separately for this provider/ })
      )
      await user.click(
        screen.getByRole('button', { name: 'Save model prices' })
      )
      await waitFor(() => expect(onChange).toHaveBeenCalledTimes(1))
      const [field, value] = onChange.mock.calls[0]
      expect(field).toBe('billing_setting.plugin_billing_expr')
      expect(JSON.parse(value)['alpha::gpt-4.1-mini']).toBe('u("seconds") * 2')
      expect(JSON.parse(value)['beta::gpt-4.1-mini']).toContain('u("clips")')
    }
    expect(put).not.toHaveBeenCalled()
  }
)
