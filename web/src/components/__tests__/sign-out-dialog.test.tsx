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
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { SignOutDialog } from '@/components/sign-out-dialog'
import { api } from '@/lib/api'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

const bundle: AuthBundle = {
  access_token: 'test-access-token',
  token_type: 'Bearer',
  access_expires_at: 9999999999,
  user: { id: 42, username: 'test-user', role: 1 },
  session: {
    sid: 'current-session',
    current: true,
    login_method: 'password',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: 9999999999,
  },
}

function renderSignOut() {
  useAuthStore.getState().auth.setBundle(bundle)
  const queryClient = new QueryClient()
  queryClient.setQueryData(['private-records'], ['private data'])
  const root = createRootRoute({ component: Outlet })
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/account',
        component: () => <SignOutDialog open onOpenChange={() => {}} />,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/sign-in',
        component: () => <div>Sign-in page</div>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/account'] }),
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { queryClient, router }
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(window, 'localStorage', 'get').mockReturnValue(window.sessionStorage)
})

afterEach(() => {
  useAuthStore.getState().auth.reset('idle')
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  window.sessionStorage.clear()
})

test('successful sign out clears authentication and private data before returning to local sign-in', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: { revoked_sid: bundle.session.sid, cookie_cleared: true },
    },
  })
  const { queryClient } = renderSignOut()

  await userEvent.click(await screen.findByRole('button', { name: 'Sign out' }))

  expect(await screen.findByText('Sign-in page')).toBeVisible()
  expect(useAuthStore.getState().auth.accessToken).toBeNull()
  expect(queryClient.getQueryData(['private-records'])).toBeUndefined()
})

test.each(['rejected', 'failed-response'])(
  'a %s logout preserves authentication and does not redirect',
  async (failure) => {
    const post = vi.spyOn(api, 'post')
    if (failure === 'rejected') {
      post.mockRejectedValue(new Error('offline'))
    } else {
      post.mockResolvedValue({
        data: { success: false, message: 'not revoked' },
      })
    }
    const { queryClient, router } = renderSignOut()

    await userEvent.click(
      await screen.findByRole('button', { name: 'Sign out' })
    )

    expect(useAuthStore.getState().auth.accessToken).toBe(bundle.access_token)
    expect(queryClient.getQueryData(['private-records'])).toEqual([
      'private data',
    ])
    expect(router.state.location.pathname).toBe('/account')
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeEnabled()
  }
)

test('a pending logout keeps credentials and disables repeated confirmation until revocation completes', async () => {
  let complete!: (response: { data: { success: boolean } }) => void
  const pending = new Promise<{ data: { success: boolean } }>((resolve) => {
    complete = resolve
  })
  vi.spyOn(api, 'post').mockReturnValue(pending)
  renderSignOut()

  await userEvent.click(await screen.findByRole('button', { name: 'Sign out' }))

  expect(screen.getByRole('button', { name: 'Sign out' })).toBeDisabled()
  expect(useAuthStore.getState().auth.accessToken).toBe(bundle.access_token)
  await act(async () => complete({ data: { success: true } }))
  await waitFor(() => expect(screen.getByText('Sign-in page')).toBeVisible())
})
