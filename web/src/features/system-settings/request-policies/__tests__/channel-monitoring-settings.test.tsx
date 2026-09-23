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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { ChannelHealthSection } from '../channel-health-section'

const clients: QueryClient[] = []
const actionContainers: HTMLDivElement[] = []

function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  clients.push(client)
  const actions = document.createElement('div')
  document.body.appendChild(actions)
  actionContainers.push(actions)
  const defaults = {
    RetryTimes: 0,
    ChannelDisableThreshold: 'invalid legacy value',
    AutomaticDisableChannelEnabled: true,
    AutomaticEnableChannelEnabled: true,
    AutomaticDisableKeywords: 'insufficient quota',
    AutomaticDisableStatusCodes: '401,403',
    AutomaticRetryStatusCodes: '429,500-599',
    'monitor_setting.auto_update_balance_enabled': false,
    'monitor_setting.auto_update_balance_minutes': 60,
    'monitor_setting.auto_test_channel_enabled': true,
    'monitor_setting.auto_test_channel_minutes': 10,
    'monitor_setting.channel_test_concurrency': 1,
    'monitor_setting.channel_test_mode': 'passive_recovery' as const,
  }
  render(
    <QueryClientProvider client={client}>
      <SettingsPageProvider actionsContainer={actions}>
        <ChannelHealthSection defaultValues={defaults} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
  return actions
}

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  actionContainers.splice(0).forEach((container) => container.remove())
})

describe('channel monitoring settings', () => {
  test('shows idle monitoring controls and explains activity timing without obsolete channel state controls', () => {
    renderSettings()

    expect(
      screen.getByRole('switch', { name: 'Scheduled channel tests' })
    ).toBeChecked()
    expect(
      screen.getByRole('spinbutton', { name: 'Idle test interval (minutes)' })
    ).toHaveValue(10)
    expect(
      screen.getByRole('spinbutton', { name: 'Channel test concurrency' })
    ).toHaveValue(1)
    expect(
      screen.queryByRole('combobox', { name: 'Channel test mode' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('switch', { name: 'Re-enable on success' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('spinbutton', { name: 'Disable threshold (seconds)' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('switch', { name: 'Disable on failure' })
    ).toBeChecked()
    expect(
      screen.getByText(
        'Test idle models on each channel, excluding manually disabled channels. Results update monitoring only.'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Successful and failed real calls restart the timer for that channel and model only. Automatic tests wait for the full idle interval; manual tests run immediately.'
      )
    ).toBeVisible()
  })

  test('saves the idle interval using the existing monitoring option without writing legacy health-check settings', async () => {
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValue({ data: { success: true, data: { options: {} } } })
    renderSettings()

    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Idle test interval (minutes)' }),
      { target: { value: '15' } }
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/request_policy',
        {
          options: { 'monitor_setting.auto_test_channel_minutes': '15' },
        }
      )
    )
  })
  test('saves scheduled balance monitoring and its interval for channels allowing balance queries', async () => {
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValue({ data: { success: true, data: { options: {} } } })
    renderSettings()
    expect(
      screen.getByText(
        'Refresh accounts only for channels with balance queries enabled, and retain balance and usage history. Channel status is unchanged.'
      )
    ).toBeVisible()
    fireEvent.click(
      screen.getByRole('switch', { name: 'Automatically refresh balances' })
    )
    fireEvent.change(
      screen.getByRole('spinbutton', {
        name: 'Balance refresh interval (minutes)',
      }),
      { target: { value: '30' } }
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/request_policy',
        {
          options: {
            'monitor_setting.auto_update_balance_enabled': 'true',
            'monitor_setting.auto_update_balance_minutes': '30',
          },
        }
      )
    )
  })

  test('rejects a balance interval above seven days without saving', async () => {
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValue({ data: { success: true, data: { options: {} } } })
    renderSettings()
    fireEvent.change(
      screen.getByRole('spinbutton', {
        name: 'Balance refresh interval (minutes)',
      }),
      { target: { value: '10081' } }
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(
      await screen.findByText('Balance interval must not exceed 7 days')
    ).toBeVisible()
    expect(patch).not.toHaveBeenCalled()
  })
})
