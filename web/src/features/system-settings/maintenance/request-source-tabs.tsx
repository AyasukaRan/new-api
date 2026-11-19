import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { BuiltinRequestSources } from './request-source-builtins'
import type { RequestSourceCondition } from './request-source-rules'
import { UnidentifiedRequestSources } from './request-source-unidentified'

export function SourceRecognitionTabs(props: {
  children: ReactNode
  onCreate?: (conditions: RequestSourceCondition[]) => void
}) {
  const { t } = useTranslation()
  const [tab, setTab] = useState('custom')
  return (
    <Tabs value={tab} onValueChange={setTab} className='min-w-0 gap-4'>
      <TabsList
        aria-label={t('Request source recognition')}
        className='max-w-full overflow-x-auto'
      >
        <TabsTrigger value='custom'>{t('Custom rules')}</TabsTrigger>
        <TabsTrigger value='builtin'>{t('Built-in recognition')}</TabsTrigger>
        <TabsTrigger value='unidentified'>
          {t('Unidentified requests')}
        </TabsTrigger>
      </TabsList>
      <TabsContent value='custom' className='space-y-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'The first matching enabled rule wins. All conditions in a rule must match. If none match, built-in recognition applies. Changes affect new requests only; other nodes refresh within 60 seconds.'
          )}
        </p>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Names are fixed display labels, not values extracted from headers. Sensitive headers cannot be used. Regular expressions are validated by the server using RE2.'
          )}
        </p>
        {props.children}
      </TabsContent>
      <TabsContent value='builtin'>
        <BuiltinRequestSources />
      </TabsContent>
      <TabsContent value='unidentified'>
        <UnidentifiedRequestSources
          onCreate={
            props.onCreate
              ? (conditions) => {
                  props.onCreate?.(conditions)
                  setTab('custom')
                }
              : undefined
          }
        />
      </TabsContent>
    </Tabs>
  )
}
