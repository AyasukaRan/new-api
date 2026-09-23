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
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { OAuthSection } from '../oauth-section'

const defaults: ComponentProps<typeof OAuthSection>['defaultValues'] = {
  GitHubOAuthEnabled: false,
  GitHubClientId: '',
  GitHubClientSecret: '',
  'discord.enabled': false,
  'discord.client_id': '',
  'discord.client_secret': '',
  'oidc.enabled': false,
  'oidc.display_name': '',
  'oidc.client_id': '',
  'oidc.client_secret': '',
  'oidc.well_known': '',
  'oidc.authorization_endpoint': '',
  'oidc.token_endpoint': '',
  'oidc.user_info_endpoint': '',
  TelegramOAuthEnabled: false,
  'telegram.client_id': '',
  'telegram.client_secret': '',
  LinuxDOOAuthEnabled: false,
  LinuxDOClientId: '',
  LinuxDOClientSecret: '',
  LinuxDOMinimumTrustLevel: '',
  WeChatAuthEnabled: false,
  WeChatServerAddress: '',
  WeChatServerToken: '',
  WeChatAccountQRCodeImageURL: '',
}

const clients: QueryClient[] = []
const containers: HTMLDivElement[] = []

async function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  clients.push(client)
  const actions = document.createElement('div')
  document.body.appendChild(actions)
  containers.push(actions)
  const route = createRootRoute({
    component: () => (
      <SettingsPageProvider actionsContainer={actions}>
        <OAuthSection
          defaultValues={defaults}
          serverAddress='https://app.example/new-api'
        />
      </SettingsPageProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('tab', { name: 'GitHub' })
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  clients.splice(0).forEach((client) => client.clear())
  containers.splice(0).forEach((container) => container.remove())
})

test('provides standard OAuth settings and saves OIDC configuration independently', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  await renderSettings()

  expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
    'GitHub',
    'Discord',
    'OIDC',
    'Telegram',
    'LinuxDO',
    'WeChat',
  ])
  await userEvent.click(screen.getByRole('tab', { name: 'OIDC' }))
  expect(screen.getByRole('switch', { name: 'Enable OIDC' })).toBeVisible()
  const input = screen.getByRole('textbox', { name: 'OIDC Display Name' })
  fireEvent.change(input, { target: { value: 'Example Identity' } })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))

  await waitFor(() =>
    expect(put).toHaveBeenCalledExactlyOnceWith('/api/option/', {
      key: 'oidc.display_name',
      value: 'Example Identity',
    })
  )
})
