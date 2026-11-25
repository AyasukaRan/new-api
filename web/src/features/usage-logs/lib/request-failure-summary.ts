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
import type { LogOtherData } from '../types'

export interface RequestFailureSummary {
  label: string
  statusCode?: number
  contextLimit?: number
  requestedTokens?: number
}

// Source keys keep historical errors classifiable after the UI translates them.
export function summarizeRequestFailure(
  error?: string,
  metadata?: LogOtherData | null
): RequestFailureSummary {
  const saved = metadata?.error_summary
  if (
    saved &&
    typeof saved.label === 'string' &&
    typeof saved.matched === 'boolean'
  ) {
    return {
      label: saved.label || 'Request failed',
      statusCode: saved.status_code,
      contextLimit: saved.context_limit,
      requestedTokens: saved.requested_tokens,
    }
  }
  const parts = Array.isArray(metadata?.content_parts)
    ? metadata.content_parts
    : []
  const text = [
    metadata?.stream_status?.end_error,
    error,
    metadata?.error_code,
    metadata?.error_type,
    ...parts
      .filter((part) => typeof part?.key === 'string')
      .slice(0, 10)
      .map((part) => [part.key, ...Object.values(part.params ?? {})].join(' ')),
  ]
    .filter(Boolean)
    .join(' ')
    .slice(0, 16384)
  const status =
    /(?:status_code["']?\s*[=:]|"status"\s*:|HTTP\/?[\d.]*\s+)\s*([45]\d{2})\b/i.exec(
      text
    )
  const result: RequestFailureSummary = { label: 'Request failed' }
  if (
    typeof metadata?.status_code === 'number' &&
    Number.isInteger(metadata.status_code) &&
    metadata.status_code >= 400 &&
    metadata.status_code <= 599
  ) {
    result.statusCode = metadata.status_code
  } else if (status) result.statusCode = Number(status[1])

  if (
    /context[_ ](?:length[_ ]exceeded|window[_ ]exceeded)|maximum context length|prompt is too long|input (?:is |tokens? .{0,40})?too (?:long|large)|上下文.{0,20}(?:超|过长)|(?:输入|提示词).{0,12}(?:过长|超出.*限制)/i.test(
      text
    )
  ) {
    result.label = 'Context length exceeded'
    const limit = /maximum context length (?:is|of)\s*([\d,]+)\s*tokens/i.exec(
      text
    )
    const requested = /(?:you requested|requested)\s*([\d,]+)\s*tokens/i.exec(
      text
    )
    const contextLimit = limit
      ? Number(limit[1].replaceAll(',', ''))
      : undefined
    const requestedTokens = requested
      ? Number(requested[1].replaceAll(',', ''))
      : undefined
    if (Number.isSafeInteger(contextLimit)) result.contextLimit = contextLimit
    if (Number.isSafeInteger(requestedTokens)) {
      result.requestedTokens = requestedTokens
    }
    return result
  }
  if (/invalid.{0,16}signature.{0,24}thinking|思考回放关联不匹配/i.test(text)) {
    result.label = 'Thinking replay mismatch'
  } else if (/unsupported image_url format/i.test(text)) {
    result.label = 'Unsupported image format'
  } else if (/failed to fetch url/i.test(text)) {
    result.label = 'Resource fetch failed'
  } else if (
    /thinking mode does not support.{0,30}tool_choice|思考模式.{0,20}(?:tool_choice|工具选择)/i.test(
      text
    )
  ) {
    result.label = 'Unsupported tool choice'
  } else if (
    /reasoning_(?:text|content).{0,100}(?:must|missing|required)|思考内容.{0,12}(?:缺失|回传)/i.test(
      text
    )
  ) {
    result.label = 'Missing reasoning content'
  } else if (
    /invalid schema for function|invalid.{0,20}(?:tool|function).{0,20}schema/i.test(
      text
    )
  ) {
    result.label = 'Invalid tool definition'
  } else if (
    /response_format.{0,60}(?:unavailable|not supported|unsupported)/i.test(
      text
    )
  ) {
    result.label = 'Unsupported response format'
  } else if (/no available accounts|无可用账号/i.test(text)) {
    result.label = 'No available upstream accounts'
  } else if (
    /insufficient[_ ](?:quota|(?:account )?balance|credits)|(?:balance|credit).{0,20}(?:insufficient|exhaust|deplet)|billing_hard_limit|余额不足|额度(?:不足|耗尽)/i.test(
      text
    )
  ) {
    result.label = 'Insufficient upstream balance'
  } else if (
    /authentication fails|invalid[_ ](?:api[_ ]?key|authentication)|api key.{0,60}(?:invalid|expired)|unauthorized|密钥.{0,12}(?:无效|失效)|认证失败/i.test(
      text
    ) ||
    result.statusCode === 401
  ) {
    result.label = 'Upstream authentication failed'
  } else if (
    /missing field.{0,20}tool_call_id|tool_use.{0,120}tool_result|tool_result.{0,120}tool_use|tool[_ ]call.{0,80}(?:not found|missing|must|match)|工具.{0,20}(?:不匹配|缺失|不完整)/i.test(
      text
    )
  ) {
    result.label = 'Tool call mismatch'
  } else if (
    /content exists risk|content[_ ](?:policy|filter)|safety.{0,20}(?:block|violation)|内容.{0,12}(?:违规|拦截)/i.test(
      text
    )
  ) {
    result.label = 'Content blocked'
  } else if (
    /cf-chl-|challenge-platform|security verification|just a moment|captcha|安全验证|人机验证/i.test(
      text
    )
  ) {
    result.label = 'Upstream security challenge'
  } else if (
    /model[_ ]not[_ ]found|model.{0,60}(?:not exist|not available|not supported|does not exist)|no available channel|模型.{0,12}(?:不存在|不可用|不支持)|无可用渠道/i.test(
      text
    )
  ) {
    result.label = 'Model unavailable'
  } else if (
    metadata?.stream_status?.end_reason === 'client_gone' ||
    /client (?:disconnected|connection closed)|客户端.{0,12}(?:断开|取消)/i.test(
      text
    )
  ) {
    result.label = 'Client disconnected'
  } else if (
    /timeout|timed out|deadline exceeded|超时/i.test(text) ||
    [408, 504, 524].includes(result.statusCode ?? 0) ||
    metadata?.stream_status?.end_reason === 'timeout'
  ) {
    result.label = 'Upstream timed out'
  } else if (
    /rate[_ ]limit|too many requests|请求.{0,8}频繁|限流/i.test(text) ||
    result.statusCode === 429
  ) {
    result.label = 'Rate limited'
  } else if (/upstream stream disconnected/i.test(text)) {
    result.label = 'Stream interrupted'
  } else if (
    /connection (?:refused|reset)|dial tcp|do request failed|no such host|tls handshake|network is unreachable|连接.{0,12}(?:拒绝|重置|失败)/i.test(
      text
    )
  ) {
    result.label = 'Upstream connection failed'
  } else if (
    /unexpected eof|stream.{0,30}(?:interrupt|incomplete|closed)|流.{0,8}(?:中断|不完整)/i.test(
      text
    )
  ) {
    result.label = 'Stream interrupted'
  } else if (
    /access forbidden|access denied|permission denied|拒绝访问|权限不足/i.test(
      text
    )
  ) {
    result.label = 'Upstream access denied'
  } else if (
    /overload|temporarily unavailable|service unavailable|服务.{0,10}(?:不可用|过载)/i.test(
      text
    ) ||
    [502, 503, 529].includes(result.statusCode ?? 0)
  ) {
    result.label = 'Upstream unavailable'
  } else if (result.statusCode && result.statusCode >= 500) {
    result.label = 'Upstream error'
  } else if (result.statusCode === 403) {
    result.label = 'Upstream access denied'
  } else if (
    [400, 413, 422].includes(result.statusCode ?? 0) ||
    /invalid[_ ](?:request|parameter)/i.test(text)
  ) {
    result.label = 'Invalid request'
  } else if (result.statusCode) {
    result.label = 'Upstream request rejected'
  }
  return result
}
