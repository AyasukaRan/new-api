import type { RequestTraceLeg } from '../types'
import { normalizeTraceImage } from './request-trace-image'

export function isTraceOcr(leg: RequestTraceLeg): boolean {
  return (
    leg.format === 'deepseek_tools' &&
    /\/v1\/tools\/ocr\/?$/.test(leg.url.split(/[?#]/, 1)[0])
  )
}

type OcrPayload =
  | {
      kind: 'result'
      pages: { number: number; text: string; finishReason?: string }[]
      inputTokens?: number
      outputTokens?: number
    }
  | { kind: 'error'; message: string }
  | { kind: 'image'; src?: string }
  | { kind: 'unavailable' }

function object(value: unknown): Record<string, unknown> | undefined {
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return undefined
}

export function parseTraceOcr(body: string, isInput: boolean): OcrPayload {
  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    if (!isInput || !/^\s*data:image\//i.test(body)) {
      return { kind: 'unavailable' }
    }
    parsed = body
  }
  const doc = object(parsed)
  if (isInput) {
    const image =
      typeof parsed === 'string'
        ? parsed
        : (doc?.image_url ?? doc?.image ?? doc?.file)
    const fields = object(image)
    const value = fields?.url ?? fields?.data ?? image
    if (typeof value === 'string') {
      return {
        kind: 'image',
        src: normalizeTraceImage(value, fields?.media_type).src,
      }
    }
    return { kind: 'unavailable' }
  }
  const reason = object(doc?.error)?.message
  if (typeof reason === 'string' && reason) {
    return { kind: 'error', message: reason }
  }
  if (
    doc?.object !== 'ocr.result' ||
    !Array.isArray(doc.pages) ||
    doc.pages.length === 0 ||
    doc.pages.length !== doc.page_count
  ) {
    return { kind: 'unavailable' }
  }
  const pages = []
  for (const item of doc.pages) {
    const page = object(item)
    if (typeof page?.text !== 'string') return { kind: 'unavailable' }
    pages.push({
      number: pages.length + 1,
      text: page.text,
      finishReason:
        typeof page.finish_reason === 'string' ? page.finish_reason : undefined,
    })
  }
  const usage = object(doc.usage)
  return {
    kind: 'result',
    pages,
    inputTokens:
      typeof usage?.input_tokens === 'number' ? usage.input_tokens : undefined,
    outputTokens:
      typeof usage?.output_tokens === 'number'
        ? usage.output_tokens
        : undefined,
  }
}
