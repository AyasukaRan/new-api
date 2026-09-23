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
import { useQueryClient } from '@tanstack/react-query'
import { Loader2, RefreshCw } from 'lucide-react'
import { useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  CodeBlock,
  CodeBlockCopyButton,
} from '@/components/ai-elements/code-block'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { getCodexUsage, updateChannelBalance } from '../../api'
import { channelsQueryKeys, parseChannelSettings } from '../../lib'
import { ChannelBalanceSummary } from '../channel-balance-summary'
import { useChannels } from '../channels-provider'
import {
  CodexUsageDialog,
  type CodexUsageDialogData,
} from './codex-usage-dialog'

type BalanceQueryDialogProps = {
  initialRawResponse?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function BalanceQueryDialog(props: BalanceQueryDialogProps) {
  const { t } = useTranslation()
  const { currentRow, setCurrentRow } = useChannels()
  const queryClient = useQueryClient()
  const [isQuerying, setIsQuerying] = useState(false)
  const [rawResponse, setRawResponse] = useState<string | null>(
    props.initialRawResponse ?? null
  )
  const [codexUsageResponse, setCodexUsageResponse] =
    useState<CodexUsageDialogData | null>(null)
  const requestGeneration = useRef(0)

  const isCodex = currentRow?.type === 57
  const balanceQueryDisabled =
    !isCodex &&
    parseChannelSettings(currentRow?.setting)?.balance_query_disabled === true

  const handleQueryCodexUsage = async () => {
    const row = currentRow
    if (!row || !props.open) return
    const generation = ++requestGeneration.current
    setIsQuerying(true)
    try {
      const res = await getCodexUsage(row.id)
      if (generation !== requestGeneration.current) return
      if (!res.success) {
        throw createServerError(res, t('Failed to fetch usage'))
      }
      setCodexUsageResponse(res)
    } catch (error: unknown) {
      if (generation !== requestGeneration.current) return
      handleServerError(error, t('Failed to fetch usage'))
    } finally {
      if (generation === requestGeneration.current) setIsQuerying(false)
    }
  }

  useLayoutEffect(() => {
    // Each opening/channel gets its own request generation. A response from a
    // closed dialog must never replace the shared selected channel or usage.
    requestGeneration.current += 1
    setIsQuerying(false)
    setRawResponse(props.open ? (props.initialRawResponse ?? null) : null)
    setCodexUsageResponse(null)
    if (isCodex && props.open) void handleQueryCodexUsage()
    return () => {
      requestGeneration.current += 1
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, currentRow?.id, isCodex, props.initialRawResponse])

  if (!currentRow) return null

  const handleQueryBalance = async () => {
    if (!props.open || balanceQueryDisabled) return
    const generation = ++requestGeneration.current
    setIsQuerying(true)
    try {
      const response = await updateChannelBalance(currentRow.id)
      void queryClient.invalidateQueries({
        queryKey: channelsQueryKeys.lists(),
      })
      void queryClient.invalidateQueries({
        queryKey: ['channel-monitoring', currentRow.id],
      })
      if (generation !== requestGeneration.current) return
      if (response.balance_monitor) {
        setCurrentRow({
          ...currentRow,
          balance_monitor: response.balance_monitor,
          balance: response.balance_monitor.balance ?? currentRow.balance,
          balance_updated_time: response.balance_monitor.balance_updated_time,
        })
      }
      if (response.partial) {
        toast.warning(
          t(
            'Some accounts could not be queried. The total retains the last complete balance.'
          )
        )
        setRawResponse(null)
      } else if (response.success && response.balance != null) {
        const newBalance = response.balance
        const now =
          response.balance_monitor?.balance_updated_time ??
          currentRow.balance_updated_time

        toast.success(t('Balance updated successfully'))

        // Update currentRow immediately with new balance and timestamp
        setCurrentRow({
          ...currentRow,
          balance_monitor: response.balance_monitor,
          balance: newBalance,
          balance_updated_time: now,
        })

        setRawResponse(null)
      } else if (response.success && response.raw_response !== undefined) {
        setRawResponse(response.raw_response)
      } else {
        toast.error(
          response.message === 'channel balance query is disabled'
            ? t(
                'Balance queries are disabled. Previously recorded balances are retained.'
              )
            : response.message || t('Failed to query balance')
        )
      }
    } catch (error: unknown) {
      if (generation !== requestGeneration.current) return
      handleServerError(error, t('Failed to query balance'))
    } finally {
      if (generation === requestGeneration.current) setIsQuerying(false)
    }
  }

  const handleClose = () => {
    requestGeneration.current += 1
    setIsQuerying(false)
    setRawResponse(null)
    setCodexUsageResponse(null)
    props.onOpenChange(false)
  }

  if (isCodex) {
    return (
      <CodexUsageDialog
        open={props.open}
        onOpenChange={(v) => {
          if (!v) handleClose()
        }}
        channelName={currentRow.name}
        channelId={currentRow.id}
        response={codexUsageResponse}
        onRefresh={handleQueryCodexUsage}
        isRefreshing={isQuerying}
      />
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleClose}
      title={t('Query Balance')}
      description={
        <>
          {t('Update balance for:')}
          <strong>{currentRow.name}</strong>
        </>
      }
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <Button variant='outline' onClick={handleClose} disabled={isQuerying}>
          {t('Close')}
        </Button>
      }
    >
      <div className='space-y-4 py-4'>
        {rawResponse !== null ? (
          <>
            <Alert>
              <AlertTitle>{t('Balance response not recognized')}</AlertTitle>
              <AlertDescription>
                {t(
                  'The upstream response is valid JSON, but it does not match the OpenAI credit_summary format. The channel balance was not updated.'
                )}
              </AlertDescription>
            </Alert>
            <CodeBlock
              code={rawResponse}
              language='json'
              maxExpandedLines={24}
              showLineNumbers
              title={t('Upstream JSON response')}
            >
              <CodeBlockCopyButton />
            </CodeBlock>
          </>
        ) : (
          <ChannelBalanceSummary
            queryDisabled={balanceQueryDisabled}
            monitor={
              currentRow.balance_monitor ?? {
                balance: currentRow.balance_updated_time
                  ? currentRow.balance
                  : null,
                balance_updated_time: currentRow.balance_updated_time,
                checked_at: currentRow.balance_updated_time,
                known_balance: currentRow.balance,
                partial: false,
                success: true,
                configuration_changed: false,
                key_balances: [],
              }
            }
          />
        )}

        {/* Balance Update Button */}
        <Button
          className='w-full'
          onClick={handleQueryBalance}
          disabled={isQuerying || balanceQueryDisabled}
        >
          {isQuerying && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
          {!isQuerying && <RefreshCw className='mr-2 h-4 w-4' />}
          {isQuerying ? t('Querying...') : t('Update Balance')}
        </Button>
      </div>
    </Dialog>
  )
}
