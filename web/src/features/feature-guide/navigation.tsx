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
import { Link } from '@tanstack/react-router'
import { BookOpen, ChevronDown, Search } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

import type { GuideArticle } from './content'

interface GuideNavigationProps {
  articles: GuideArticle[]
  articleId: string
}

export function GuideNavigation(props: GuideNavigationProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  const [mobileOpen, setMobileOpen] = useState(false)
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  const filtered = props.articles.filter((article) => {
    const text = [
      article.title,
      article.summary,
      article.markdown,
      ...(article.keywords ?? []),
    ]
      .join(' ')
      .toLocaleLowerCase()
    return terms.every((term) => text.includes(term))
  })
  const sections = [
    { id: 'overview', label: t('Getting started') },
    { id: 'user', label: t('User guide') },
    { id: 'admin', label: t('Administrator guide') },
  ] as const

  return (
    <aside className='border-border border-b lg:border-r lg:border-b-0'>
      <Collapsible
        open={isDesktop || mobileOpen}
        onOpenChange={setMobileOpen}
        className='lg:sticky lg:top-24'
      >
        <CollapsibleTrigger
          render={
            <Button
              variant='ghost'
              className='my-2 w-full justify-between lg:hidden'
            />
          }
        >
          <span className='flex items-center gap-2'>
            <BookOpen aria-hidden='true' />
            {t('Guide contents')}
          </span>
          <ChevronDown
            aria-hidden='true'
            className={cn(mobileOpen && 'rotate-180')}
          />
        </CollapsibleTrigger>
        <CollapsibleContent>
          <nav
            aria-label={t('Guide contents')}
            className='space-y-6 py-5 lg:max-h-[calc(100svh-7rem)] lg:overflow-y-auto lg:pr-6'
          >
            <div className='space-y-3'>
              <div className='hidden items-center gap-2 text-sm font-semibold lg:flex'>
                <BookOpen aria-hidden='true' className='text-primary size-4' />
                {t('Feature guide')}
              </div>
              <div className='relative'>
                <Search
                  aria-hidden='true'
                  className='text-muted-foreground pointer-events-none absolute top-2.5 left-3 size-4'
                />
                <Input
                  ref={searchRef}
                  type='search'
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  aria-label={t('Search the guide')}
                  placeholder={t('Search the guide')}
                  className='h-9 pl-9'
                />
              </div>
              {terms.length > 0 && (
                <p role='status' className='text-muted-foreground text-xs'>
                  {t('Search results: {{count}}', { count: filtered.length })}
                </p>
              )}
            </div>
            {filtered.length === 0 ? (
              <EmptyState
                icon={Search}
                title={t('No results found')}
                className='min-h-40 p-2'
                action={
                  <Button
                    variant='outline'
                    onClick={() => {
                      setQuery('')
                      searchRef.current?.focus()
                    }}
                  >
                    {t('Clear search')}
                  </Button>
                }
              />
            ) : (
              sections.map((section) => {
                const articles = filtered.filter(
                  (article) => article.section === section.id
                )
                if (articles.length === 0) return null
                return (
                  <div key={section.id} className='space-y-2'>
                    <h2 className='text-muted-foreground px-2 text-xs font-medium'>
                      {section.label}
                    </h2>
                    <ul className='space-y-1'>
                      {articles.map((article) => (
                        <li key={article.id}>
                          <Link
                            to='/guide'
                            search={{ article: article.id }}
                            aria-current={
                              props.articleId === article.id
                                ? 'page'
                                : undefined
                            }
                            onClick={() => {
                              if (props.articleId !== article.id)
                                setMobileOpen(false)
                            }}
                            className={cn(
                              'focus-visible:ring-ring block rounded-lg px-3 py-2 text-sm leading-relaxed outline-none focus-visible:ring-2',
                              props.articleId === article.id
                                ? 'bg-primary/10 text-primary font-medium'
                                : 'text-muted-foreground hover:bg-muted hover:text-foreground'
                            )}
                            lang='zh-CN'
                          >
                            {article.title}
                          </Link>
                        </li>
                      ))}
                    </ul>
                  </div>
                )
              })
            )}
          </nav>
        </CollapsibleContent>
      </Collapsible>
    </aside>
  )
}
