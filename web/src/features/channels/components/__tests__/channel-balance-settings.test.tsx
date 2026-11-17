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
import { zodResolver } from '@hookform/resolvers/zod'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { useAuthStore } from '@/stores/auth-store'

import {
  buildSettingJSON,
  channelFormSchema,
  transformChannelToFormDefaults,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelSchema } from '../../types'
import { ChannelBalanceSettings } from '../channel-balance-settings'

function BalanceSettingsFixture(props: {
  setting?: string
  onSave: (setting: Record<string, unknown>) => void
  onSaveValues?: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: transformChannelToFormDefaults(
      channelSchema.parse({
        id: 1,
        type: 1,
        name: 'Upstream',
        key: 'example',
        models: 'gpt-5',
        setting: props.setting,
        status: 1,
        created_time: 0,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
      })
    ),
  })
  return (
    <Form {...form}>
      <form
        noValidate
        onSubmit={form.handleSubmit((values) => {
          props.onSave(JSON.parse(buildSettingJSON(values)))
          props.onSaveValues?.(values)
        })}
      >
        <ChannelBalanceSettings control={form.control} />
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

async function renderSettings(
  setting?: string,
  onSaveValues?: (values: ChannelFormValues) => void
) {
  const onSave = vi.fn()
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <BalanceSettingsFixture
          setting={setting}
          onSave={onSave}
          onSaveValues={onSaveValues}
        />
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
  return onSave
}
afterEach(() => useAuthStore.setState(useAuthStore.getInitialState(), true))

describe('channel balance query settings', () => {
  test('an OpenAI channel can query New API balances without changing its relay type or recharge multiplier', async () => {
    const user = userEvent.setup()
    const onSaveValues = vi.fn()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_query_type: 'openai',
        balance_recharge_multiplier: 10,
      }),
      onSaveValues
    )
    const provider = await screen.findByRole('combobox', {
      name: 'Balance Query API',
    })
    expect(provider).toHaveTextContent('OpenAI')
    await user.click(provider)
    await user.click(screen.getByRole('option', { name: 'New API' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(onSave.mock.calls[0][0]).toMatchObject({
      balance_query_type: 'newapi',
      balance_recharge_multiplier: 10,
    })
    expect(onSaveValues).toHaveBeenCalledWith(
      expect.objectContaining({ type: 1, balance_recharge_multiplier: 10 })
    )
  })

  test('the display multiplier can be saved while queries are disabled and reset to its default', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_query_disabled: true,
        balance_recharge_multiplier: 3,
        upstream_cost_ratio: 0.3,
      })
    )
    const multiplier = await screen.findByRole('spinbutton', {
      name: 'Balance recharge multiplier',
    })
    expect(multiplier).toBeEnabled()
    expect(multiplier).toHaveValue(3)
    await user.clear(multiplier)
    await user.type(multiplier, '2')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(onSave.mock.calls[0][0]).toMatchObject({
      balance_query_disabled: true,
      balance_recharge_multiplier: 2,
      upstream_cost_ratio: 0.3,
    })
    await user.clear(multiplier)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
    expect(onSave.mock.calls[1][0]).not.toHaveProperty(
      'balance_recharge_multiplier'
    )
    expect(onSave.mock.calls[1][0].upstream_cost_ratio).toBe(0.3)
  })

  test('a zero recharge multiplier prevents saving and raw email thresholds remain explicit', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_email_enabled: true,
        balance_email_threshold: 10,
      })
    )
    const multiplier = await screen.findByRole('spinbutton', {
      name: 'Balance recharge multiplier',
    })
    await user.clear(multiplier)
    await user.type(multiplier, '0')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText(
        'Enter a finite recharge multiplier between 0.000001 and 1000000.'
      )
    ).toBeVisible()
    expect(multiplier).toHaveAttribute('aria-invalid', 'true')
    expect(
      screen.getByRole('spinbutton', { name: 'Low balance threshold' })
    ).toHaveValue(10)
    expect(
      screen.getByText(
        /Alert thresholds and email balances use the original upstream amount/
      )
    ).toBeVisible()
    expect(onSave).not.toHaveBeenCalled()
  })

  test('an existing channel defaults to enabled and can disable queries without losing provider configuration', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_query_type: 'openai',
        balance_query_base_url: 'https://billing.example',
      })
    )
    const toggle = await screen.findByRole('switch', {
      name: 'Balance queries',
    })
    expect(toggle).toBeChecked()
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
    expect(
      screen.getByRole('combobox', { name: 'Balance Query API' })
    ).toBeDisabled()
    expect(
      screen.getByRole('textbox', { name: 'Balance Query Address' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          balance_query_disabled: true,
          balance_query_type: 'openai',
          balance_query_base_url: 'https://billing.example',
        })
      )
    )
  })

  test('a disabled channel can re-enable queries using the keyboard and keep its configured address', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_query_disabled: true,
        balance_query_base_url: 'https://billing.example',
      })
    )
    const toggle = await screen.findByRole('switch', {
      name: 'Balance queries',
    })
    expect(toggle).not.toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).toBeChecked()
    const address = screen.getByRole('textbox', {
      name: 'Balance Query Address',
    })
    expect(address).toBeEnabled()
    expect(address).toHaveValue('https://billing.example')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onSave.mock.calls[0][0].balance_query_disabled).not.toBe(true)
  })

  test('balance emails require an explicit threshold and preserve other channel settings', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({ balance_query_type: 'openai' })
    )
    const email = await screen.findByRole('switch', {
      name: 'Balance email notifications',
    })
    expect(email).not.toBeChecked()
    expect(
      screen.queryByRole('spinbutton', { name: 'Low balance threshold' })
    ).not.toBeInTheDocument()
    email.focus()
    await user.keyboard(' ')
    const threshold = screen.getByRole('spinbutton', {
      name: 'Low balance threshold',
    })
    expect(threshold).toHaveValue(null)
    const save = screen.getByRole('button', { name: 'Save' })
    await user.click(save)
    expect(
      await screen.findByText(
        'Enter a finite balance threshold of zero or more.'
      )
    ).toBeVisible()
    expect(onSave).not.toHaveBeenCalled()
    await user.type(threshold, '12.5')
    await user.click(save)
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(onSave.mock.calls[0][0]).toMatchObject({
      balance_email_enabled: true,
      balance_email_threshold: 12.5,
      balance_query_type: 'openai',
    })
  })

  test('a legacy alert without a threshold can be fixed with a zero threshold or turned off', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({ balance_email_enabled: true })
    )
    const threshold = await screen.findByRole('spinbutton', {
      name: 'Low balance threshold',
    })
    const save = screen.getByRole('button', { name: 'Save' })
    await user.click(save)
    expect(
      await screen.findByText(
        'Enter a finite balance threshold of zero or more.'
      )
    ).toBeVisible()
    await user.type(threshold, '0')
    await user.click(save)
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(onSave.mock.calls[0][0]).toMatchObject({
      balance_email_enabled: true,
      balance_email_threshold: 0,
    })
    await user.clear(threshold)
    await user.click(
      screen.getByRole('switch', { name: 'Balance email notifications' })
    )
    await user.click(save)
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
    expect(onSave.mock.calls[1][0]).not.toHaveProperty('balance_email_enabled')
    expect(onSave.mock.calls[1][0]).not.toHaveProperty(
      'balance_email_threshold'
    )
  })

  test('a negative alert threshold prevents saving the channel', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({ balance_email_enabled: true })
    )
    const threshold = await screen.findByRole('spinbutton', {
      name: 'Low balance threshold',
    })
    await user.type(threshold, '-1')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText(
        'Enter a finite balance threshold of zero or more.'
      )
    ).toBeVisible()
    expect(threshold).toHaveAttribute('aria-invalid', 'true')
    expect(onSave).not.toHaveBeenCalled()
  })

  test.each(['Balance email notifications', 'Balance queries'])(
    'an invalid threshold does not block saving after turning off %s',
    async (label) => {
      const user = userEvent.setup()
      const onSave = await renderSettings(
        JSON.stringify({ balance_email_enabled: true })
      )
      const threshold = await screen.findByRole('spinbutton', {
        name: 'Low balance threshold',
      })
      await user.type(threshold, '-1')
      const save = screen.getByRole('button', { name: 'Save' })
      await user.click(save)
      expect(
        await screen.findByText(
          'Enter a finite balance threshold of zero or more.'
        )
      ).toBeVisible()
      await user.click(screen.getByRole('switch', { name: label }))
      await user.click(save)
      await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
      expect(onSave.mock.calls[0][0]).not.toHaveProperty(
        'balance_email_threshold'
      )
      expect(onSave.mock.calls[0][0]).not.toHaveProperty(
        'balance_email_enabled'
      )
    }
  )

  test('disabled balance queries suspend email controls and retain their threshold for re-enabling', async () => {
    const user = userEvent.setup()
    const onSave = await renderSettings(
      JSON.stringify({
        balance_email_enabled: true,
        balance_email_threshold: 1.25,
        balance_query_disabled: true,
      })
    )
    const email = await screen.findByRole('switch', {
      name: 'Balance email notifications',
    })
    expect(email).toBeChecked()
    expect(email).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('spinbutton', { name: 'Low balance threshold' })
    ).toBeDisabled()
    await user.click(screen.getByRole('switch', { name: 'Balance queries' }))
    await user.click(email)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(onSave.mock.calls[0][0]).not.toHaveProperty('balance_email_enabled')
    expect(onSave.mock.calls[0][0]).toMatchObject({
      balance_email_threshold: 1.25,
    })
  })

  test('super administrators can open the existing global balance refresh settings without leaving their draft', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'admin', role: 100 })
    await renderSettings()
    const link = await screen.findByRole('link', {
      name: 'Global balance refresh settings',
    })
    expect(link).toHaveAttribute(
      'href',
      '/system-settings/request-policies/health'
    )
    expect(link).toHaveAttribute('target', '_blank')
  })
})
