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
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import type { ErrorRecognitionRule } from './error-recognition-rules'

export function RulePreview(props: { rules: ErrorRecognitionRule[] }) {
  const { t } = useTranslation()
  const [error, setError] = useState('')
  const [statusCode, setStatusCode] = useState('')
  const preview = useMutation({
    mutationFn: async () => {
      const response = await api.post<{
        success: boolean
        data: { label: string; matched: boolean; rule_id?: string }
      }>('/api/option/error_recognition_rules/preview', {
        rules: props.rules,
        error,
        ...(statusCode ? { status_code: Number(statusCode) } : {}),
      })
      return requireServerSuccess(response.data).data
    },
    meta: { errorToast: false },
  })
  return (
    <form
      className='space-y-3'
      onSubmit={(event) => {
        event.preventDefault()
        preview.mutate()
      }}
    >
      <p className='text-muted-foreground text-sm'>
        {t(
          'Test the current draft without saving or sending an upstream request.'
        )}
      </p>
      <Label htmlFor='error-preview-text'>{t('Original error')}</Label>
      <Textarea
        id='error-preview-text'
        value={error}
        maxLength={16384}
        rows={5}
        disabled={preview.isPending}
        onChange={(event) => {
          setError(event.target.value)
          preview.reset()
        }}
      />
      <div className='flex flex-wrap items-center gap-3'>
        <Input
          type='number'
          min={400}
          max={599}
          step={1}
          className='w-44'
          aria-label={t('Status Code')}
          placeholder={t('Status Code')}
          value={statusCode}
          disabled={preview.isPending}
          onChange={(event) => {
            setStatusCode(event.target.value)
            preview.reset()
          }}
        />
        <Button type='submit' disabled={!error.trim() || preview.isPending}>
          {t('Test match')}
        </Button>
      </div>
      {preview.isError && (
        <ErrorState description={getServerErrorMessage(preview.error)} />
      )}
      {preview.data && (
        <div
          role='status'
          className='bg-muted/30 space-y-1 rounded-lg p-3 text-sm'
        >
          <p>{preview.data.matched ? t(preview.data.label) : t('Unmatched')}</p>
          {preview.data.rule_id && (
            <code className='text-muted-foreground text-xs'>
              {preview.data.rule_id}
            </code>
          )}
        </div>
      )}
    </form>
  )
}
