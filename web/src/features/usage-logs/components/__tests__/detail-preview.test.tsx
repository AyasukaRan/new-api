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
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { useState } from 'react'
import { I18nextProvider } from 'react-i18next'
import {
  afterAll,
  afterEach,
  beforeEach,
  describe,
  expect,
  test,
  vi,
} from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import type { UsageLog } from '../../data/schema'
import type {
  LogOtherData,
  LogSource,
  UsageLogDetailSelection,
} from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { DetailsDialog } from '../dialogs/details-dialog'

vi.mock('@lobehub/icons', () => ({}))
vi.hoisted(() => {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  })
})
afterAll(() => vi.unstubAllGlobals())

function makeLog(other: LogOtherData, type = 2): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'wan2.5-i2v-preview',
    quota: 5000,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: '',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function DetailPreview(props: {
  other: LogOtherData
  isAdmin: boolean
  type?: number
  source?: LogSource
  columnId?: 'content' | 'prompt_tokens' | 'created_at' | 'request_details'
  createdAt?: number
}) {
  const [selectedLog, setSelectedLog] =
    useState<UsageLogDetailSelection | null>(null)
  const table = useReactTable({
    data: [
      { ...makeLog(props.other, props.type), created_at: props.createdAt ?? 1 },
    ],
    columns: useCommonLogsColumns(props.isAdmin, setSelectedLog, props.source),
    getCoreRowModel: getCoreRowModel(),
  })
  const cell = table
    .getRowModel()
    .rows[0].getAllCells()
    .find((item) => item.column.id === (props.columnId ?? 'content'))
  if (!cell) throw new Error('The log must have a content column')
  return (
    <>
      {flexRender(cell.column.columnDef.cell, cell.getContext())}
      {selectedLog && (
        <DetailsDialog
          open
          log={selectedLog.log}
          initialView={selectedLog.initialView}
          source={props.source}
          isAdmin={props.isAdmin}
          isRoot={false}
          onOpenChange={(open) => {
            if (!open) setSelectedLog(null)
          }}
        />
      )}
    </>
  )
}
const plugin = {
  key: 'incho',
  name: 'Incho',
  version: '1.0.1',
  author: { name: 'Plugin maintainer' },
}
const previousConfig = useSystemConfigStore.getState().config
let client: QueryClient
const i18n = createInstance()
beforeEach(async () => {
  await i18n.init({
    lng: 'en',
    resources: { en },
    interpolation: { escapeValue: false },
  })
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  client.setQueryData(
    ['pricing'],
    { data: [], vendors: [] },
    { updatedAt: Date.now() + 60_000 }
  )
})
afterEach(() => {
  client.clear()
  useSystemConfigStore.getState().setConfig(previousConfig)
})
function renderPreview(other: LogOtherData, isAdmin = true, type?: number) {
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <DetailPreview other={other} isAdmin={isAdmin} type={type} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  return screen.getByRole('button', { name: /./ })
}

test.each([true, false])(
  'only shows the upstream ID in administrator details (isAdmin %s)',
  async (isAdmin) => {
    const log = { ...makeLog({}), upstream_request_id: 'private-upstream-id' }
    render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>
          <DetailsDialog
            log={log}
            isAdmin={isAdmin}
            isRoot={false}
            open
            onOpenChange={() => undefined}
          />
        </QueryClientProvider>
      </I18nextProvider>
    )
    expect(await screen.findByText('req-1')).toBeVisible()
    if (isAdmin) {
      expect(screen.getByText('private-upstream-id')).toBeVisible()
    } else {
      expect(screen.queryByText('private-upstream-id')).not.toBeInTheDocument()
    }
  }
)

test.each([
  ['2025-12-31', '23:59:59'],
  ['2026-01-01', '00:00:01'],
])(
  'shows the full local date and time for %s at %s without an action',
  (date, time) => {
    render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>
          <DetailPreview
            isAdmin
            other={{}}
            columnId='created_at'
            createdAt={new Date(`${date}T${time}`).valueOf() / 1000}
          />
        </QueryClientProvider>
      </I18nextProvider>
    )
    expect(screen.getByText(date)).toBeVisible()
    expect(screen.getByText(time)).toHaveAttribute(
      'datetime',
      `${date}T${time}`
    )
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  }
)

test.each(['hover', 'keyboard'])(
  'explains the request details icon on %s',
  async (interaction) => {
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>
          <DetailPreview isAdmin other={{}} columnId='request_details' />
        </QueryClientProvider>
      </I18nextProvider>
    )
    const trigger = screen.getByRole('button', { name: 'Details · req-1' })
    expect(trigger).toHaveAttribute('aria-haspopup', 'dialog')
    if (interaction === 'hover') {
      await user.hover(trigger)
    } else {
      await user.tab()
      expect(trigger).toHaveFocus()
    }
    expect(await screen.findByText('View details')).toBeVisible()
    expect(trigger).toHaveAccessibleName('Details · req-1')
  }
)

function DetailProviders({ children }: { children: React.ReactNode }) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    </I18nextProvider>
  )
}

test('keeps the recorded decision order in technical details and scopes diagnostics by viewer', () => {
  const log = makeLog({
    group_ratio: 1,
    admin_info: {
      request_policy: [
        {
          attempt: 1,
          elapsed_ms: 28,
          rule: 'primary-route',
          decision: {
            action: 'failure',
            reason: 'failure',
            source: 'upstream',
          },
          status: 503,
        },
        {
          attempt: 2,
          elapsed_ms: 141,
          rule: 'secondary-route',
          decision: { action: 'stop', reason: 'stop', source: 'gateway' },
        },
      ],
    },
    root_info: { node_name: 'private-gateway-node' },
  })
  const props = {
    open: true,
    log,
    initialView: 'technical' as const,
    onOpenChange: () => undefined,
  }
  const { rerender } = render(<DetailsDialog {...props} isAdmin isRoot />, {
    wrapper: DetailProviders,
  })
  const technical = within(
    screen.getByRole('tabpanel', { name: 'Technical details' })
  )
  const decisions = technical
    .getAllByRole('listitem')
    .filter((item) =>
      /primary-route|secondary-route/.test(item.textContent ?? '')
    )
  expect(decisions).toHaveLength(2)
  expect(decisions[0]).toHaveTextContent('primary-route')
  expect(decisions[0]).toHaveTextContent('28 ms')
  expect(decisions[1]).toHaveTextContent('secondary-route')
  expect(decisions[1]).toHaveTextContent('141 ms')
  expect(technical.getByText('private-gateway-node')).toBeVisible()
  rerender(<DetailsDialog {...props} isAdmin isRoot={false} />)
  expect(technical.getByText(/primary-route/)).toBeVisible()
  expect(technical.queryByText('private-gateway-node')).not.toBeInTheDocument()
  rerender(<DetailsDialog {...props} isAdmin={false} isRoot={false} />)
  expect(
    technical.queryByText(/primary-route|secondary-route|private-gateway-node/)
  ).not.toBeInTheDocument()
  expect(
    technical.getByText('No diagnostics recorded for this request')
  ).toBeVisible()
  for (const label of [
    'Request ID',
    'Token',
    'Group',
    'Billing Details',
    'Token Breakdown',
  ]) {
    expect(
      technical.queryByText(label, { exact: true })
    ).not.toBeInTheDocument()
  }
})

test('technical details show recorded model settings without defaulting omitted parameters', () => {
  const parameters = {
    temperature: 0,
    top_p: 0.0025,
    seed: '9223372036854775807',
    parallel_tool_calls: false,
    reasoning_effort: 'high',
    prompt: 'private prompt',
  }
  const props = {
    open: true,
    isAdmin: false,
    isRoot: false,
    initialView: 'technical' as const,
    onOpenChange: () => undefined,
  }
  const { rerender } = render(
    <DetailsDialog
      {...props}
      log={makeLog({ model_parameters: parameters })}
    />,
    { wrapper: DetailProviders }
  )
  const technical = within(
    screen.getByRole('tabpanel', { name: 'Technical details' })
  )
  expect(technical.getByText('Model parameters')).toBeVisible()
  for (const [label, value] of [
    ['Temperature', '0'],
    ['Top P', '0.0025'],
    ['Seed', '9223372036854775807'],
    ['Parallel tool calls', 'Disabled'],
    ['Reasoning Effort', 'high'],
  ]) {
    expect(technical.getByText(label).nextElementSibling).toHaveTextContent(
      value
    )
  }
  expect(technical.queryByText('private prompt')).not.toBeInTheDocument()
  expect(technical.queryByText('Max output tokens')).not.toBeInTheDocument()

  rerender(
    <DetailsDialog {...props} log={makeLog({ reasoning_effort: 'medium' })} />
  )
  expect(
    technical.getByText('Recorded reasoning effort').nextElementSibling
  ).toHaveTextContent('medium')
  expect(technical.queryByText('Temperature')).not.toBeInTheDocument()
  rerender(
    <DetailsDialog {...props} log={makeLog({ reasoning_effort: 'adaptive' })} />
  )
  expect(
    technical.getByText('Thinking mode').nextElementSibling
  ).toHaveTextContent('adaptive')
  rerender(<DetailsDialog {...props} log={makeLog({})} />)
  expect(technical.queryByText('Model parameters')).not.toBeInTheDocument()
})

test('zero-charge failures keep restricted billing evidence available only to administrators', async () => {
  const other: LogOtherData = {
    admin_info: {
      quota_saturation: {
        op: 'round',
        kind: 'overflow',
        original: 3e9,
        clamped: 2147483647,
      },
    },
  }
  const log = { ...makeLog(other, 5), quota: 0 }
  const props = {
    open: true,
    log,
    isRoot: false,
    onOpenChange: () => undefined,
  }
  const { rerender } = render(<DetailsDialog {...props} isAdmin />, {
    wrapper: DetailProviders,
  })
  await userEvent.click(screen.getByRole('tab', { name: 'Cost breakdown' }))
  const evidence = screen.getByRole('button', { name: 'Billing parameters' })
  expect(evidence).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByText('Quota clamped')).not.toBeInTheDocument()
  await userEvent.click(evidence)
  expect(screen.getByText('Clamped to').nextElementSibling).toHaveTextContent(
    '2147483647'
  )
  rerender(<DetailsDialog {...props} isAdmin={false} />)
  expect(
    screen.queryByRole('tab', { name: 'Cost breakdown' })
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Clamped to')).not.toBeInTheDocument()
})

test('keeps log details open when the parent refreshes with unchanged data', async () => {
  const other = { model_price: 0.25 }
  const { rerender } = render(<DetailPreview other={other} isAdmin />, {
    wrapper: DetailProviders,
  })
  fireEvent.click(screen.getByRole('button', { name: 'Per-call · $0.25' }))
  expect(await screen.findByRole('dialog')).toBeVisible()
  rerender(<DetailPreview other={other} isAdmin />)
  expect(screen.getByRole('dialog')).toBeVisible()
})

test.each([
  ['peak', 'Peak hours', '高峰时段', 'peak.png'],
  ['off_peak', 'Off-peak hours', '非高峰时段', 'off-peak.png'],
  ['standard', 'Standard', '标准', 'standard.png'],
  ['base', 'Standard', '标准', 'standard.png'],
])(
  'shows the %s tier as a localized icon with a tooltip',
  async (tier, label, translated, assetName) => {
    const user = userEvent.setup()
    i18n.addResourceBundle('zh', 'translation', zh.translation)
    const preview = renderPreview({
      billing_mode: 'tiered_expr',
      matched_tier: tier,
      expr_b64: btoa(`tier("${tier}", p * 2 + c * 8)`),
    })
    const icon = within(preview).getByRole('img', { name: label })
    expect(icon).toBeVisible()
    expect(icon).toHaveClass('h-7', 'w-8')
    expect(icon).toHaveAttribute(
      'data-tier',
      tier === 'base' ? 'standard' : tier
    )
    const image = icon.querySelector('img')
    expect(image).toHaveClass('h-full', 'w-full')
    expect(image).toHaveAttribute('width', '32')
    expect(image).toHaveAttribute('height', '28')
    expect(image).toHaveAttribute('src', expect.stringContaining(assetName))
    expect(image).toHaveAttribute('alt', '')
    expect(image).toHaveAttribute('aria-hidden', 'true')
    expect(icon.querySelector('svg')).toBeNull()
    expect(preview).toHaveTextContent('$2 / $8/M')
    expect(preview).not.toHaveTextContent(tier)
    await act(() => i18n.changeLanguage('zh'))
    const translatedPreview = screen.getByRole('button', {
      name: new RegExp(translated),
    })
    expect(
      within(translatedPreview).getByRole('img', { name: translated })
    ).toBeVisible()
    await user.hover(translatedPreview)
    expect(await screen.findByText(translated)).toBeVisible()
    fireEvent.click(translatedPreview)
    expect(await screen.findByRole('dialog')).toBeVisible()
  }
)

test('keeps pure cached Claude input visible in the desktop Token column', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <DetailPreview
          other={{ claude: true, cache_tokens: 1000 }}
          isAdmin
          columnId='prompt_tokens'
        />
      </QueryClientProvider>
    </I18nextProvider>
  )
  expect(
    screen.getByRole('term', { name: 'Cache read tokens' }).nextElementSibling
  ).toHaveTextContent('1,000')
  expect(
    screen.getByRole('progressbar', { name: 'Cache hit rate' })
  ).toHaveAttribute('aria-valuenow', '100')
  expect(screen.getByText('100%')).toBeVisible()
})

test.each([
  {
    name: 'fixed expression zero price',
    other: {
      billing_mode: 'tiered_expr',
      billing_unit: 'request' as const,
      fixed_price: 0,
      matched_tier: 'free',
      expr_b64: btoa('tier("free", fixed(0))'),
    },
    expected: 'free · Per-call $0/request',
  },
  {
    name: 'fixed expression trace outside the display grammar',
    other: {
      billing_mode: 'tiered_expr',
      billing_unit: 'request' as const,
      fixed_price: 0.01,
      matched_tier: 'priority',
      expr_b64: btoa(
        'param("fast") == true ? tier("priority", fixed(0.01)) : tier("tokens", p * 2)'
      ),
    },
    expected: 'priority · Per-call $0.01/request',
  },
  {
    name: 'parsed token expression',
    other: {
      billing_mode: 'tiered_expr',
      matched_tier: 'standard',
      expr_b64: btoa('tier("standard", p * 2 + c * 10)'),
    },
    expected: '$2 / $10/M',
  },
  {
    name: 'custom token tier',
    other: {
      billing_mode: 'tiered_expr',
      matched_tier: 'normal',
      expr_b64: btoa('tier("normal", p * 2 + c * 10)'),
    },
    expected: 'normal · $2 / $10/M',
  },
  {
    name: 'per-call',
    other: { model_price: 0.25 },
    expected: 'Per-call · $0.25',
  },
  {
    name: 'standard',
    other: { model_ratio: 1, completion_ratio: 2 },
    expected: '$2 / $4/M',
  },
  {
    name: 'zero price fallback',
    other: { model_price: 0, group_ratio: 1 },
    expected: 'Group Ratio 1x',
  },
  { name: 'missing price fallback', other: {}, expected: '—' },
])('$name stays visible without a plugin counter', ({ other, expected }) => {
  const preview = renderPreview({
    ...other,
    admin_info: { task_plugin: plugin },
  })
  expect(preview.textContent).toBe(expected)
})

test.each([
  {
    tier: 'standard',
    billingUnit: 'token' as const,
    fixedPrice: undefined,
    expected: 'Dynamic Pricing · standard',
  },
  {
    tier: 'long_context',
    billingUnit: 'token' as const,
    fixedPrice: undefined,
    expected: 'Dynamic Pricing · long_context',
  },
  {
    tier: 'free_small_output',
    billingUnit: 'request' as const,
    fixedPrice: 0,
    expected: 'free_small_output · Per-call $0/request',
  },
])(
  'nested expression preserves the recorded $tier settlement in the list',
  ({ tier, billingUnit, fixedPrice, expected }) => {
    const expression = `len <= 272000
      ? (c > 5
        ? tier("standard", p * 2 + c * 10 + cr * 0.1 + cc * 2.5)
        : tier("free_small_output", fixed(0)))
      : (c > 5
        ? tier("long_context", p * 5 + c * 22.5 + cr * 0.5)
        : tier("free_small_output", fixed(0)))`
    const preview = renderPreview({
      billing_mode: 'tiered_expr',
      billing_unit: billingUnit,
      fixed_price: fixedPrice,
      matched_tier: tier,
      expr_b64: btoa(expression),
    })
    expect(preview.textContent).toBe(expected)
  }
)

test.each([
  { name: 'missing expression', expr: undefined },
  { name: 'invalid encoded expression', expr: '%' },
  {
    name: 'tier absent from the parsed expression',
    expr: btoa('tier("different", p * 99 + c * 99)'),
  },
])(
  'token pricing with $name preserves the saved tier without guessing prices',
  ({ expr }) => {
    const preview = renderPreview({
      billing_mode: 'tiered_expr',
      matched_tier: 'recorded',
      expr_b64: expr,
    })
    expect(preview.textContent).toBe('Dynamic Pricing · recorded')
  }
)

test.each([false, true])(
  'dynamic pricing without a saved tier uses a localized neutral fallback for task=%s',
  async (isTask) => {
    i18n.addResourceBundle('zh', 'translation', zh.translation)
    const preview = renderPreview({
      is_task: isTask,
      billing_mode: 'tiered_expr',
      expr_b64: btoa(
        isTask
          ? 'tier("base", u("clips") * 0.25)'
          : 'tier("base", p * 2 + c * 10)'
      ),
    })
    expect(preview.textContent).toBe(
      'Dynamic Pricing · Special billing expression'
    )
    await act(() => i18n.changeLanguage('zh'))
    expect(
      screen.getByRole('button', { name: '动态计费 · 特殊计费表达式' })
    ).toBeVisible()
  }
)

test('top-up content_parts render in the viewer language', async () => {
  i18n.addResourceBundle('zh', 'translation', {
    'Online top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}':
      '在线充值成功，充值额度：{{quota}}，支付金额：{{amount}}',
  })
  await i18n.changeLanguage('zh')
  const preview = renderPreview(
    {
      content_parts: [
        {
          key: 'Online top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}',
          params: { quota: '$10.000000', amount: '10.00' },
        },
      ],
    },
    false,
    1
  )
  expect(preview.textContent).toBe(
    '在线充值成功，充值额度：$10.000000，支付金额：10.00'
  )
})

test('quota saturation remains first and only billing adds to the counter', () => {
  const preview = renderPreview({
    model_price: 0.25,
    admin_info: {
      task_plugin: plugin,
      quota_saturation: {
        op: 'round',
        kind: 'overflow',
        original: 3e9,
        clamped: 2147483647,
      },
    },
  })
  expect(preview.textContent).toBe('Quota clamped+1')
})

test.each([true, false])(
  'plugin information in the opened dialog respects admin=%s',
  async (isAdmin) => {
    const preview = renderPreview(
      { model_price: 0.25, admin_info: { task_plugin: plugin } },
      isAdmin
    )
    expect(preview.textContent).toBe('Per-call · $0.25')
    fireEvent.click(preview)
    const dialog = within(await screen.findByRole('dialog'))
    if (isAdmin) {
      expect(dialog.getByText('Incho')).toBeVisible()
      expect(dialog.getByText('1.0.1')).toBeVisible()
      expect(dialog.getByText('Plugin maintainer')).toBeVisible()
    } else {
      expect(dialog.queryByText('Incho')).not.toBeInTheDocument()
      expect(dialog.queryByText('Plugin maintainer')).not.toBeInTheDocument()
    }
  }
)

test.each([
  {
    expression: 'tier("music", u("clips") * 0.25)',
    tier: 'music',
    expected: 'music · clips $0.25/unit',
  },
  {
    expression:
      'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)',
    tier: 'pro',
    expected: 'pro · seconds $0.8/second',
  },
  {
    expression: 'tier("tokens", u("tokens") * 9.8 / 1000000)',
    tier: 'tokens',
    expected: 'tokens · tokens $9.8/1M token',
  },
  {
    expression: 'tier("free", u("clips") * 0)',
    tier: 'free',
    expected: 'free · clips $0/unit',
  },
  {
    expression: 'tier("mixed", 0.1 + u("clips") * 0.25 + u("units") * 0.14)',
    tier: 'mixed',
    expected:
      'mixed · clips $0.25/unit · units $0.14/credit · Additional charge $0.1/request',
  },
])(
  'task expression $tier shows its recorded unit price',
  ({ expression, tier, expected }) => {
    client.setQueryData(['pricing'], {
      data: [
        {
          model_name: 'wan2.5-i2v-preview',
          billing_expr: 'tier("current", u("clips") * 99)',
          billing_usage_schema: {
            clips: { type: 'number', unit: 'count' },
            seconds: { type: 'number', unit: 'second' },
            tokens: { type: 'number', unit: 'token' },
            units: { type: 'number', unit: 'credit' },
            mode: { enum: ['pro', 'std'] },
          },
        },
      ],
      vendors: [],
    })
    const preview = renderPreview({
      is_task: true,
      billing_mode: 'tiered_expr',
      expr_b64: Buffer.from(expression).toString('base64'),
      matched_tier: tier,
      model_price: 0,
      admin_info: { task_plugin: plugin },
    })
    expect(preview.textContent).toBe(expected)
  }
)

test('task log prices use localized unit labels from pricing metadata', async () => {
  client.setQueryData(['pricing'], {
    data: [
      {
        model_name: 'wan2.5-i2v-preview',
        billing_usage_schema: {
          images: {
            type: 'number',
            unit: 'count',
            unitLabel: { en: 'image', zh: '张' },
          },
        },
      },
    ],
    vendors: [],
  })
  const preview = renderPreview({
    is_task: true,
    billing_mode: 'tiered_expr',
    expr_b64: btoa('tier("images", u("images") * 0.25)'),
    matched_tier: 'images',
  })
  expect(preview).toHaveTextContent('images · images $0.25/image')
  await act(() => i18n.changeLanguage('zh-CN'))
  expect(
    screen.getByRole('button', { name: /images · images/ })
  ).toHaveTextContent('images · images $0.25/张')
})

test('task log prices select the executing provider’s schema', () => {
  client.setQueryData(['pricing'], {
    data: [
      {
        model_name: 'wan2.5-i2v-preview',
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
        billing_plugin_variants: [
          {
            plugin_key: 'beta',
            plugin_name: 'Beta',
            billing_expr: 'tier("images", u("images") * 0.25)',
            billing_usage_schema: {
              images: {
                type: 'number',
                unit: 'count',
                unitLabel: { en: 'image' },
              },
            },
          },
        ],
      },
    ],
    vendors: [],
  })
  const preview = renderPreview({
    is_task: true,
    billing_mode: 'tiered_expr',
    expr_b64: btoa('tier("images", u("images") * 0.25)'),
    matched_tier: 'images',
    admin_info: {
      task_plugin: { key: 'beta', name: 'Beta', version: '1.0.0' },
    },
  })
  expect(preview).toHaveTextContent('images · images $0.25/image')
})

test.each(['missing schema', 'unsupported expression', 'unknown tier'])(
  'task pricing with %s preserves the saved tier without guessing prices',
  (scenario) => {
    if (scenario !== 'missing schema') {
      client.setQueryData(['pricing'], {
        data: [
          {
            model_name: 'wan2.5-i2v-preview',
            billing_usage_schema: { clips: { type: 'number', unit: 'count' } },
          },
        ],
        vendors: [],
      })
    }
    const expression =
      scenario === 'unsupported expression'
        ? 'tier("music", max(u("clips"), 1) * 0.25)'
        : 'tier("music", u("clips") * 0.25)'
    const preview = renderPreview({
      is_task: true,
      billing_mode: 'tiered_expr',
      expr_b64: Buffer.from(expression).toString('base64'),
      matched_tier: scenario === 'unknown tier' ? 'old' : 'music',
    })
    expect(preview.textContent).toBe(
      `Dynamic Pricing · ${scenario === 'unknown tier' ? 'old' : 'music'}`
    )
  }
)

test.each([true, false])(
  'recorded cost and gross profit in log details require administrator view: %s',
  async (isAdmin) => {
    const other: LogOtherData = {
      model_price: 0.25,
      admin_info: {
        cost: {
          cost_ratio: 1.3,
          base_quota: 5000,
          cost_quota: 6500,
          revenue_quota: 5000,
          profit_quota: -1500,
          estimated: true,
        },
      },
    }
    fireEvent.click(renderPreview(other, isAdmin))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('tab', { name: 'Cost breakdown' }))
    if (isAdmin) {
      expect(dialog.getByText('Estimated upstream cost')).toBeVisible()
      expect(dialog.getByText('$0.013')).toBeVisible()
      expect(
        dialog.getByText('Gross profit').nextElementSibling
      ).toHaveTextContent(/-\$0\.003$/)
      await userEvent.click(
        dialog.getByRole('button', { name: 'Billing parameters' })
      )
      expect(dialog.getByText('1.3000x')).toBeVisible()
    } else {
      expect(
        dialog.queryByText('Estimated upstream cost')
      ).not.toBeInTheDocument()
      expect(dialog.queryByText('1.3000x')).not.toBeInTheDocument()
      expect(dialog.queryByText('-$0.003')).not.toBeInTheDocument()
    }
  }
)

test('test log details never show profit even when metadata contains an estimate', async () => {
  const other: LogOtherData = {
    model_price: 0.25,
    admin_info: {
      cost: {
        cost_ratio: 1.3,
        base_quota: 5000,
        cost_quota: 6500,
        revenue_quota: 5000,
        profit_quota: -1500,
        estimated: true,
      },
    },
  }
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <DetailPreview other={other} isAdmin source='test' />
      </QueryClientProvider>
    </I18nextProvider>
  )
  fireEvent.click(screen.getByRole('button', { name: /Per-call/ }))
  const dialog = within(await screen.findByRole('dialog'))
  expect(dialog.queryByText('Gross profit')).not.toBeInTheDocument()
})

test.each([true, false])(
  'upstream bill details hide private costs outside administrator view: %s',
  async (isAdmin) => {
    const other: LogOtherData = {
      model_price: 0.25,
      admin_info: {
        cost: {
          cost_ratio: 1.3,
          base_quota: 5000,
          cost_quota: 6500,
          revenue_quota: 5000,
          profit_quota: -1500,
          estimated: false,
          source: 'upstream_bill',
        },
      },
    }
    fireEvent.click(renderPreview(other, isAdmin))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('tab', { name: 'Cost breakdown' }))
    if (isAdmin) {
      expect(dialog.getByText('Upstream cost')).toBeVisible()
      expect(dialog.getByText('$0.013')).toBeVisible()
      expect(
        dialog.getByText('Gross profit').nextElementSibling
      ).toHaveTextContent(/-\$0\.003$/)
    } else {
      expect(dialog.queryByText('Upstream cost')).not.toBeInTheDocument()
      expect(dialog.queryByText('$0.013')).not.toBeInTheDocument()
      expect(dialog.queryByText('Gross profit')).not.toBeInTheDocument()
    }
  }
)

test('an admin loads the captured request only when opening the row and can retry a failed fetch', async () => {
  useAuthStore.getState().auth.setUser({ id: 77, role: 10, username: 'admin' })
  const ref = 'a'.repeat(64)
  const get = vi
    .spyOn(api, 'get')
    .mockRejectedValueOnce(new Error('unavailable'))
    .mockResolvedValue({
      data: { success: true, data: 'captured request text' },
    })
  try {
    const open = renderPreview({
      model_price: 1,
      admin_info: { request_body_ref: ref, request_body_truncated: 123456 },
    })
    expect(get).not.toHaveBeenCalledWith(
      '/api/log/request_body',
      expect.anything()
    )
    fireEvent.click(open)
    expect(get).not.toHaveBeenCalled()
    fireEvent.click(
      await screen.findByRole('button', { name: 'Request Content' })
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText('captured request text')
    ).toBeVisible()
    expect(within(dialog).getByText(/123456 bytes/)).toBeVisible()
    expect(get).toHaveBeenCalledWith(
      '/api/log/request_body',
      expect.objectContaining({
        params: { request_id: 'req-1', created_at: 1, ref },
      })
    )
  } finally {
    get.mockRestore()
    useAuthStore.getState().auth.reset()
  }
})

describe('request content disclosure in log details', () => {
  beforeEach(() => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 77, role: 10, username: 'admin' })
  })
  afterEach(() => {
    vi.restoreAllMocks()
    useAuthStore.getState().auth.reset()
  })

  test('opens captured prompts on demand and distinguishes local estimates from recorded usage', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          status: 'partial',
          reasons: ['referenced_history'],
          estimated_tokens: 30,
          categories: {
            system: {
              count: 1,
              estimated_tokens: 20,
              texts: ['System line 1\nSystem line 2'],
            },
            developer: {
              count: 1,
              estimated_tokens: 10,
              texts: ['Developer text'],
            },
          },
        },
      },
    })
    const log = {
      ...makeLog({ admin_info: { trace_id: 'recorded-input' } }),
      prompt_tokens: 50,
    }
    const props = {
      log,
      open: true,
      isRoot: false,
      onOpenChange: () => undefined,
    }
    const view = render(<DetailsDialog {...props} isAdmin />, {
      wrapper: DetailProviders,
    })
    const card = within(screen.getByRole('region', { name: 'Token Breakdown' }))
    expect(get).not.toHaveBeenCalled()
    await userEvent.click(
      card.getByRole('button', { name: 'Input composition and prompts' })
    )
    expect(
      await card.findByText('System line 1 System line 2')
    ).toHaveTextContent('System line 1 System line 2')
    expect(get).toHaveBeenCalledWith(
      '/api/log/input_analysis',
      expect.objectContaining({
        params: expect.objectContaining({
          trace_id: 'recorded-input',
          request_id: log.request_id,
        }),
      })
    )
    expect(card.getByText('≈ 30')).toBeVisible()
    expect(
      card.getByText('Referenced history is outside this request.')
    ).toBeVisible()
    await userEvent.click(
      card.getByRole('button', { name: /Developer instructions/ })
    )
    expect(card.getByText('Developer text')).toBeVisible()
    view.rerender(<DetailsDialog {...props} isAdmin={false} />)
    expect(screen.queryByText('Developer text')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Input composition and prompts' })
    ).not.toBeInTheDocument()
  })

  test('trace-backed details omit duplicated JSON and truncation warnings without loading the trace until requested', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: { legs: [] } } })
    fireEvent.click(
      renderPreview({
        model_price: 1,
        admin_info: {
          trace_id: 'complete-original',
          request_body: 'Legacy summary',
          request_body_ref: 'a'.repeat(64),
          request_body_truncated: 3312033,
        },
      })
    )
    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Request Content' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Legacy summary')).not.toBeInTheDocument()
    expect(screen.queryByText(/Truncated for storage/)).not.toBeInTheDocument()
    const dialog = within(screen.getByRole('dialog'))
    for (const name of ['Overview', 'Cost breakdown', 'Technical details']) {
      await userEvent.click(dialog.getByRole('tab', { name }))
      expect(
        dialog.getByRole('button', { name: 'View request trace' })
      ).toBeVisible()
    }
    expect(get).not.toHaveBeenCalled()
    await userEvent.click(dialog.getByRole('tab', { name: 'Timing breakdown' }))
    expect(get).toHaveBeenCalledTimes(1)
    expect(get).toHaveBeenCalledWith(
      '/api/log/trace',
      expect.objectContaining({
        params: { trace_id: 'complete-original', metadata_only: true },
      })
    )
    await userEvent.click(
      dialog.getByRole('button', { name: 'View request trace' })
    )
    expect(
      await screen.findByText('No trace records are available.')
    ).toBeVisible()
    expect(get).toHaveBeenCalledWith(
      '/api/log/trace',
      expect.objectContaining({
        params: { trace_id: 'complete-original', metadata_only: true },
      })
    )
    expect(get).not.toHaveBeenCalledWith(
      '/api/log/request_body',
      expect.anything()
    )
  })

  test.each([undefined, '   '])(
    'historical inline content starts collapsed with trace ID %s and preserves its storage warning only when expanded',
    async (traceId) => {
      const get = vi.spyOn(api, 'get')
      fireEvent.click(
        renderPreview({
          model_price: 1,
          admin_info: {
            request_body: 'Only historical original',
            trace_id: traceId,
            request_body_truncated: 123456,
          },
        })
      )
      const disclosure = await screen.findByRole('button', {
        name: 'Request Content',
      })
      expect(disclosure).toHaveAttribute('aria-expanded', 'false')
      expect(
        screen.queryByText('Only historical original')
      ).not.toBeInTheDocument()
      expect(
        screen.queryByText(/Truncated for storage/)
      ).not.toBeInTheDocument()
      await userEvent.click(disclosure)
      expect(disclosure).toHaveAttribute('aria-expanded', 'true')
      expect(screen.getByText('Only historical original')).toBeVisible()
      expect(screen.getByText(/Truncated for storage/)).toHaveTextContent(
        '123456'
      )
      await userEvent.click(disclosure)
      expect(
        screen.queryByText('Only historical original')
      ).not.toBeInTheDocument()
      expect(get).not.toHaveBeenCalled()
    }
  )

  test('collapsing or closing historical content cancels its on-demand request', async () => {
    const signals: AbortSignal[] = []
    const get = vi
      .spyOn(api, 'get')
      .mockImplementation(async (_url, config) => {
        signals.push(config?.signal as AbortSignal)
        return new Promise(() => undefined)
      })
    fireEvent.click(
      renderPreview({
        model_price: 1,
        admin_info: { request_body_ref: 'b'.repeat(64) },
      })
    )
    const disclosure = await screen.findByRole('button', {
      name: 'Request Content',
    })
    expect(get).not.toHaveBeenCalled()
    await userEvent.click(disclosure)
    await waitFor(() => expect(signals).toHaveLength(1))
    await userEvent.click(disclosure)
    await waitFor(() => expect(signals[0].aborted).toBe(true))
    await userEvent.click(disclosure)
    await waitFor(() => expect(signals).toHaveLength(2))
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(signals[1].aborted).toBe(true))
  })

  test('changing historical records with a reused row ID resets the disclosure before loading the next reference', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: 'First row summary' } })
    const wrapper = ({ children }: { children: React.ReactNode }) => (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      </I18nextProvider>
    )
    const first = makeLog({ admin_info: { request_body_ref: 'a'.repeat(64) } })
    const second = makeLog({ admin_info: { request_body_ref: 'b'.repeat(64) } })
    const { rerender } = render(
      <DetailsDialog
        log={first}
        initialView='technical'
        isAdmin
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />,
      { wrapper }
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Request Content' })
    )
    expect(await screen.findByText('First row summary')).toBeVisible()
    expect(get).toHaveBeenCalledTimes(1)
    rerender(
      <DetailsDialog
        log={second}
        initialView='technical'
        isAdmin
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    )
    expect(
      screen.getByRole('button', { name: 'Request Content' })
    ).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('First row summary')).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledTimes(1)
  })

  test.each([true, false])(
    'non-administrators have no historical content entry even with isAdmin prop %s',
    async (isAdmin) => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 88, role: 1, username: 'user' })
      const get = vi.spyOn(api, 'get')
      fireEvent.click(
        renderPreview(
          {
            model_price: 1,
            admin_info: {
              request_body: 'Private history',
              request_body_ref: 'c'.repeat(64),
            },
          },
          isAdmin
        )
      )
      expect(await screen.findByRole('dialog')).toBeVisible()
      expect(
        screen.queryByRole('button', { name: 'Request Content' })
      ).not.toBeInTheDocument()
      expect(screen.queryByText('Private history')).not.toBeInTheDocument()
      expect(get).not.toHaveBeenCalled()
    }
  )
})

test.each([
  ['Upstream temporarily unavailable', 'Upstream unavailable'],
  [
    "status_code=400, This model's maximum context length is 1048576 tokens. However, you requested 1770481 tokens (1770481 in the messages, 0 in the completion). Please reduce the length of the messages or completion. (request_id: historical-request)",
    'Context length exceeded',
  ],
])(
  'failed request previews summarize %s and keep the original in details',
  async (error, label) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    try {
      render(
        <QueryClientProvider client={queryClient}>
          <DetailPreview
            isAdmin={false}
            type={5}
            other={{
              content_parts: [{ key: error }],
              retry_decision: {
                action: 'stop',
                reason: 'response_already_started',
              },
            }}
          />
        </QueryClientProvider>
      )
      expect(screen.getByText(label)).toBeVisible()
      expect(screen.queryByText(error)).not.toBeInTheDocument()
      expect(
        screen.getByText(
          'Response content has already been sent; retrying could duplicate output'
        )
      ).toBeVisible()
      await userEvent.click(
        screen.getByRole('button', { name: new RegExp(label) })
      )
      const dialog = await screen.findByRole('dialog', { name: /Log Details/ })
      expect(within(dialog).getByText('Original error')).toBeVisible()
      expect(within(dialog).getByText(error)).toBeVisible()
    } finally {
      queryClient.clear()
    }
  }
)
