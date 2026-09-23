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
import { ArrowLeft, ArrowRight, BookOpen } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { RichContent } from '@/components/rich-content'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import type { GuideArticle } from './content'
import { GuideNavigation } from './navigation'

interface GuideReaderProps {
  articles: GuideArticle[]
  articleId: string
  updatedAt: string
}

export function GuideReader(props: GuideReaderProps) {
  const { t } = useTranslation()
  const articleIndex = props.articles.findIndex(
    (article) => article.id === props.articleId
  )
  const article = props.articles[articleIndex]
  const previous = props.articles[articleIndex - 1]
  const next = props.articles[articleIndex + 1]
  const articleRef = useRef<HTMLElement>(null)
  const previousId = useRef(props.articleId)

  useEffect(() => {
    if (previousId.current === props.articleId) return
    previousId.current = props.articleId
    articleRef.current?.focus({ preventScroll: true })
    articleRef.current?.scrollIntoView({ block: 'start' })
  }, [props.articleId])

  return (
    <main className='mx-auto grid max-w-[1440px] grid-cols-1 gap-x-10 px-5 pt-20 pb-12 lg:grid-cols-[240px_minmax(0,1fr)] lg:px-8 xl:grid-cols-[240px_minmax(0,1fr)_180px]'>
      <GuideNavigation articles={props.articles} articleId={props.articleId} />
      <article
        ref={articleRef}
        aria-label={t('Guide article')}
        tabIndex={-1}
        className='min-w-0 scroll-mt-24 py-8 outline-none lg:py-6'
      >
        {article ? (
          <>
            <header className='mb-9 border-b pb-7'>
              <div className='text-muted-foreground mb-4 flex flex-wrap items-center gap-2 text-xs'>
                <span>{t('Feature guide')}</span>
                <span aria-hidden='true'>/</span>
                <span lang='zh-CN'>{article.title}</span>
                <Badge variant='outline' className='ml-auto'>
                  {t('Chinese')}
                </Badge>
              </div>
              <h1
                className='text-3xl leading-tight font-semibold tracking-tight sm:text-4xl'
                lang='zh-CN'
              >
                {article.title}
              </h1>
              <p
                className='text-muted-foreground mt-4 text-base leading-7'
                lang='zh-CN'
              >
                {article.summary}
              </p>
              <div className='mt-5 flex flex-wrap items-center justify-between gap-3'>
                <p className='text-muted-foreground text-xs'>
                  {t('Last updated')}{' '}
                  <time dateTime={props.updatedAt}>{props.updatedAt}</time>
                </p>
                <CopyButton
                  value={`# ${article.title}\n\n${article.summary}\n\n${article.markdown}`}
                  aria-label={t('Copy as Markdown')}
                  variant='outline'
                  size='sm'
                >
                  {t('Copy as Markdown')}
                </CopyButton>
              </div>
            </header>
            <div lang='zh-CN'>
              <RichContent
                content={article.markdown}
                className='text-[15px] [&_h2]:mt-10 [&_h2]:border-b [&_h2]:pb-3 [&_li]:leading-7 [&_p]:leading-7 [&_pre]:max-w-full [&_table]:text-sm'
              />
            </div>
            <nav
              aria-label={t('Article navigation')}
              className='mt-12 grid gap-3 border-t pt-6 sm:grid-cols-2'
            >
              {previous && (
                <Button
                  role='link'
                  variant='outline'
                  className='h-auto justify-start gap-3 p-4 text-left whitespace-normal'
                  render={
                    <Link to='/guide' search={{ article: previous.id }} />
                  }
                >
                  <ArrowLeft aria-hidden='true' className='size-4 shrink-0' />
                  <span className='min-w-0'>
                    <span className='text-muted-foreground mb-1 block text-xs'>
                      {t('Previous article')}
                    </span>
                    <span lang='zh-CN'>{previous.title}</span>
                  </span>
                </Button>
              )}
              {next && (
                <Button
                  role='link'
                  variant='outline'
                  className='h-auto justify-end gap-3 p-4 text-right whitespace-normal sm:col-start-2'
                  render={<Link to='/guide' search={{ article: next.id }} />}
                >
                  <span className='min-w-0'>
                    <span className='text-muted-foreground mb-1 block text-xs'>
                      {t('Next article')}
                    </span>
                    <span lang='zh-CN'>{next.title}</span>
                  </span>
                  <ArrowRight aria-hidden='true' className='size-4 shrink-0' />
                </Button>
              )}
            </nav>
          </>
        ) : (
          <EmptyState
            icon={BookOpen}
            title={t('Article not found')}
            description={t(
              'Choose an article from the guide contents or return to the overview.'
            )}
            action={
              <Button
                role='link'
                render={<Link to='/guide' search={{ article: 'overview' }} />}
              >
                {t('Back to overview')}
              </Button>
            }
          />
        )}
      </article>
      <aside className='border-t py-6 lg:col-start-2 xl:col-start-3 xl:row-start-1 xl:border-t-0'>
        <div className='text-muted-foreground sticky top-24 space-y-5 text-xs leading-6'>
          <h2 className='text-foreground font-semibold'>
            {t('About this guide')}
          </h2>
          <p>
            {t(
              'Practical instructions for using this platform. Available features depend on your account permissions and site settings.'
            )}
          </p>
          <Button
            role='link'
            variant='outline'
            size='sm'
            className='w-full justify-between'
            render={<Link to='/dashboard' />}
          >
            {t('Go to Dashboard')}
            <ArrowRight aria-hidden='true' />
          </Button>
        </div>
      </aside>
    </main>
  )
}
