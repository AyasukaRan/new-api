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
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import type { GuideArticle } from '../content'
import { GuideReader } from '../reader'
import { guideSearchSchema } from '../search'

const articles: GuideArticle[] = [
  {
    id: 'overview',
    title: '开始使用',
    summary: '认识本站功能。',
    section: 'overview',
    markdown: '## 功能入口\n\n选择模型后创建令牌。',
  },
  {
    id: 'wallet',
    title: '订阅与额度',
    summary: '查看额度使用情况。',
    section: 'user',
    markdown: '## 查看订阅\n\n订阅刷新时间在额度区域显示。',
    keywords: ['quota'],
  },
  {
    id: 'channels',
    title: '渠道管理',
    summary: '检查渠道。',
    section: 'admin',
    markdown: '## 监控\n\n查看最近的渠道状态。',
  },
]

async function renderGuide(entry = '/guide', content = articles) {
  const root = createRootRoute()
  const guideRoute = createRoute({
    getParentRoute: () => root,
    path: '/guide/',
    validateSearch: guideSearchSchema,
  })
  const router = createRouter({
    routeTree: root.addChildren([guideRoute]),
    history: createMemoryHistory({ initialEntries: [entry] }),
  })
  guideRoute.update({
    component: () => {
      const search = guideRoute.useSearch<typeof router>()
      return (
        <GuideReader
          articles={content}
          articleId={search.article}
          updatedAt='2026-09-16'
        />
      )
    },
  })
  await router.load()
  render(<RouterProvider router={router} />)
  return router
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
})

describe('feature guide reader', () => {
  test('mobile users can open the contents by keyboard, search article text and focus the selected article', async () => {
    const user = userEvent.setup()
    await renderGuide()
    const toggle = await screen.findByRole('button', { name: 'Guide contents' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(
      screen.queryByRole('navigation', { name: 'Guide contents' })
    ).not.toBeInTheDocument()
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const contents = screen.getByRole('navigation', { name: 'Guide contents' })
    await user.type(
      within(contents).getByRole('searchbox', { name: 'Search the guide' }),
      '刷新时间'
    )
    expect(
      within(contents).queryByRole('link', { name: '开始使用' })
    ).not.toBeInTheDocument()
    const result = within(contents).getByRole('link', { name: '订阅与额度' })
    result.focus()
    await user.keyboard('{Enter}')
    expect(
      await screen.findByRole('heading', { level: 1, name: '订阅与额度' })
    ).toBeVisible()
    await waitFor(() =>
      expect(
        screen.getByRole('article', { name: 'Guide article' })
      ).toHaveFocus()
    )
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    expect(screen.getByRole('link', { name: '订阅与额度' })).toHaveAttribute(
      'aria-current',
      'page'
    )
  })

  test('an unmatched search can be cleared by keyboard and returns focus to the search input', async () => {
    const user = userEvent.setup()
    await renderGuide()
    await user.click(
      await screen.findByRole('button', { name: 'Guide contents' })
    )
    const contents = screen.getByRole('navigation', { name: 'Guide contents' })
    const search = within(contents).getByRole('searchbox', {
      name: 'Search the guide',
    })
    await user.type(search, 'nothing-matches-this-query')
    expect(within(contents).getByText('No results found')).toBeVisible()
    within(contents).getByRole('button', { name: 'Clear search' }).focus()
    await user.keyboard('{Enter}')
    expect(search).toHaveValue('')
    expect(search).toHaveFocus()
    expect(within(contents).getAllByRole('link')).toHaveLength(3)
  })

  test('selecting the current article by keyboard keeps its directory link focused', async () => {
    const user = userEvent.setup()
    await renderGuide()
    const toggle = await screen.findByRole('button', { name: 'Guide contents' })
    await user.click(toggle)
    const contents = screen.getByRole('navigation', { name: 'Guide contents' })
    const current = within(contents).getByRole('link', { name: '开始使用' })
    current.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(current).toHaveFocus())
    expect(
      screen.getByRole('heading', { level: 1, name: '开始使用' })
    ).toBeVisible()
  })

  test('unknown articles show a recovery action instead of rendering unrelated content', async () => {
    const user = userEvent.setup()
    await renderGuide('/guide?article=missing')
    expect(await screen.findByText('Article not found')).toBeVisible()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Back to overview' }))
    expect(
      await screen.findByRole('heading', { name: '开始使用', level: 1 })
    ).toBeVisible()
  })

  test('previous and next links navigate in reading order and retain the article in the URL', async () => {
    const user = userEvent.setup()
    const router = await renderGuide()
    const navigation = await screen.findByRole('navigation', {
      name: 'Article navigation',
    })
    expect(
      within(navigation).queryByRole('link', { name: /Previous article/ })
    ).not.toBeInTheDocument()
    await user.click(
      within(navigation).getByRole('link', { name: /Next article/ })
    )
    await screen.findByRole('heading', { name: '订阅与额度', level: 1 })
    expect(router.state.location.search).toEqual({ article: 'wallet' })
    await user.click(screen.getByRole('link', { name: /Previous article/ }))
    expect(
      await screen.findByRole('heading', { name: '开始使用', level: 1 })
    ).toBeVisible()
  })

  test('copying an article includes its title, summary and Markdown body', async () => {
    const user = userEvent.setup()
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    await renderGuide()
    await user.click(
      await screen.findByRole('button', { name: 'Copy as Markdown' })
    )
    await waitFor(() =>
      expect(copy).toHaveBeenCalledWith(
        '# 开始使用\n\n认识本站功能。\n\n## 功能入口\n\n选择模型后创建令牌。'
      )
    )
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeVisible()
  })

  test('article Markdown uses the shared sanitizer and opens safe external links without an opener', async () => {
    const content: GuideArticle[] = [
      {
        ...articles[0],
        markdown:
          '## 安全内容\n\n[官方说明](https://docs.newapi.pro/zh/docs)\n\n<a href="javascript:alert(1)">unsafe link</a><img src="x" onerror="alert(1)"><script>alert(1)</script>',
      },
    ]
    await renderGuide('/guide', content)
    const article = await screen.findByRole('article', {
      name: 'Guide article',
    })
    const safeLink = within(article).getByRole('link', { name: '官方说明' })
    expect(safeLink).toHaveAttribute('target', '_blank')
    expect(safeLink).toHaveAttribute('rel', 'noopener noreferrer')
    expect(within(article).getByText('unsafe link')).not.toHaveAttribute('href')
    expect(article.querySelector('[onerror], script')).toBeNull()
    expect(
      screen
        .queryByRole('navigation', { name: 'Article navigation' })
        ?.querySelector('a')
    ).toBeNull()
  })

  test('desktop contents remain available without expanding the mobile control', async () => {
    const originalMatchMedia = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...originalMatchMedia(query),
      matches:
        query === '(min-width: 1024px)' || originalMatchMedia(query).matches,
    }))
    await renderGuide()
    const contents = await screen.findByRole('navigation', {
      name: 'Guide contents',
    })
    expect(
      within(contents).getByRole('link', { name: '开始使用' })
    ).toHaveAttribute('aria-current', 'page')
    expect(
      within(contents).getByRole('searchbox', { name: 'Search the guide' })
    ).toBeVisible()
  })

  test.each([
    undefined,
    '',
    ['wallet'],
    12,
    'javascript:alert(1)',
    'a'.repeat(81),
  ])('invalid article search value %j falls back to the overview', (article) =>
    expect(guideSearchSchema.parse({ article })).toEqual({
      article: 'overview',
    })
  )
})
