import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { useTracePayload } from '../../hooks/use-trace-payload'
import { selectConversationLegs } from '../../lib/request-trace-conversation'
import type { RequestTraceLeg } from '../../types'
import { TraceImagePreview } from './image-dialog'
import { RequestTraceObject } from './request-trace-object'

export function RequestTraceOcr(props: {
  traceId: string
  legs: RequestTraceLeg[]
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const legs = selectConversationLegs(props.legs)
  return (
    <div className='h-full space-y-4 overflow-y-auto overscroll-contain'>
      {legs.map((leg) => (
        <OcrLeg
          key={leg.seq}
          traceId={props.traceId}
          leg={leg}
          locale={locale}
        />
      ))}
      {!legs.some((leg) => leg.direction.endsWith('_response')) && (
        <p className='text-muted-foreground text-sm'>
          {t('No OCR result was captured.')}
        </p>
      )}
    </div>
  )
}

function OcrLeg(props: {
  traceId: string
  leg: RequestTraceLeg
  locale?: string
}) {
  const { t } = useTranslation()
  const leg = props.leg
  const isInput = leg.direction.endsWith('_request')
  const query = useTracePayload(
    props.traceId,
    leg,
    'ocr',
    !(isInput && leg.has_object)
  )
  const payload = query.data?.ocr
  if (isInput && leg.has_object) {
    return <RequestTraceObject traceId={props.traceId} leg={leg} uploadedFile />
  }
  if (!leg.has_body) {
    return (
      <p className='text-muted-foreground text-sm'>
        {isInput
          ? t('The original upload was not retained.')
          : t('No OCR result was captured.')}
      </p>
    )
  }
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load the request trace')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (leg.truncated || !payload || payload.kind === 'unavailable') {
    return (
      <p className='text-muted-foreground text-sm'>
        {t('See Exchange details for the captured payload.')}
      </p>
    )
  }
  if (payload.kind === 'image') return <TraceImagePreview src={payload.src} />
  if (payload.kind === 'error') {
    return (
      <p role='alert' className='text-destructive text-sm whitespace-pre-wrap'>
        {payload.message}
      </p>
    )
  }
  return (
    <div className='space-y-4'>
      {(payload.inputTokens !== undefined ||
        payload.outputTokens !== undefined) && (
        <div className='text-muted-foreground flex flex-wrap gap-3 text-xs'>
          {payload.inputTokens !== undefined && (
            <span>
              {t('Input tokens')} ·{' '}
              {formatNumber(payload.inputTokens, props.locale)}
            </span>
          )}
          {payload.outputTokens !== undefined && (
            <span>
              {t('Output tokens')} ·{' '}
              {formatNumber(payload.outputTokens, props.locale)}
            </span>
          )}
        </div>
      )}
      {payload.pages.map((page) => (
        <section key={page.number} className='min-w-0 rounded-md border p-3'>
          <div className='mb-2 flex items-center gap-2'>
            <h4 className='text-sm font-medium'>
              {t('Page {{current}} of {{total}}', {
                current: formatNumber(page.number, props.locale),
                total: formatNumber(payload.pages.length, props.locale),
              })}
            </h4>
            {page.finishReason && page.finishReason !== 'stop' && (
              <span className='text-muted-foreground text-xs'>
                {page.finishReason}
              </span>
            )}
            {page.text && (
              <CopyButton value={page.text} className='ml-auto size-7' />
            )}
          </div>
          {page.text ? (
            <pre className='font-sans text-sm break-words whitespace-pre-wrap'>
              {page.text}
            </pre>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('No text was recognized on this page.')}
            </p>
          )}
        </section>
      ))}
    </div>
  )
}
