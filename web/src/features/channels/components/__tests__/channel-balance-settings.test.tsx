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
}) {
  const form = useForm<ChannelFormValues>({
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
        onSubmit={form.handleSubmit((values) =>
          props.onSave(
            JSON.parse(buildSettingJSON(channelFormSchema.parse(values)))
          )
        )}
      >
        <ChannelBalanceSettings control={form.control} />
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

async function renderSettings(setting?: string) {
  const onSave = vi.fn()
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <BalanceSettingsFixture setting={setting} onSave={onSave} />
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
