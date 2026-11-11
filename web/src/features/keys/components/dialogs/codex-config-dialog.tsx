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
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlock } from '@/components/ai-elements/code-block'
import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { useModelAccessScope } from '@/hooks/use-model-access-scope'
import { currentBaseUrl } from '@/lib/base-path'

import { getCodexModels } from '../../api'

interface Props {
  tokenId: number
  tokenKey: string
  onOpenChange: (open: boolean) => void
}

export default function CodexConfigDialog(props: Props) {
  const { t } = useTranslation()
  const scope = useModelAccessScope()
  const [selectedModel, setSelectedModel] = useState<string | null>(null)
  const catalog = useQuery({
    queryKey: ['codex-models', ...scope, props.tokenId],
    queryFn: ({ signal }) => getCodexModels(props.tokenKey, signal),
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const options = useMemo(
    () =>
      catalog.data?.map((model) => ({
        value: model.slug,
        label: model.slug,
      })) ?? [],
    [catalog.data]
  )
  const model = options.find((option) => option.value === selectedModel)?.value
  const baseUrl = `${currentBaseUrl()}/v1`
  const config = `model = ${JSON.stringify(model ?? '')}
model_provider = "newapi"

[model_providers.newapi]
name = "NewAPI"
base_url = ${JSON.stringify(baseUrl)}
model_catalog_url = ${JSON.stringify(`${baseUrl}/codex/models`)}
env_key = "NEWAPI_API_KEY"
wire_api = "responses"
requires_openai_auth = false

[features]
api_key_model_discovery = true
`

  return (
    <Dialog
      open
      onOpenChange={props.onOpenChange}
      title={t('Configure Codex')}
      description={t('Load models available to this API key in Codex.')}
      bodyClassName='flex flex-col gap-4'
    >
      <p className='text-muted-foreground text-sm'>
        {t(
          'Requires Codex CLI 0.156.0 or later, or a Codex app with remote model catalog support.'
        )}
      </p>
      {catalog.isPending && <LoadingState message={t('Loading models...')} />}
      {catalog.isError && (
        <ErrorState
          title={t('Failed to load Codex models')}
          description={t(
            'Check that this API key is enabled and has access to models.'
          )}
          onRetry={() => void catalog.refetch()}
        />
      )}
      {catalog.isSuccess && options.length === 0 && (
        <EmptyState
          title={t('No models available for this API key')}
          description={t(
            'Check the API key model limits and group configuration.'
          )}
        />
      )}
      {catalog.isSuccess && options.length > 0 && (
        <>
          <div className='flex flex-col gap-2'>
            <Label htmlFor='codex-primary-model'>{t('Primary Model')}</Label>
            <Combobox
              id='codex-primary-model'
              aria-label={t('Primary Model')}
              options={options}
              value={selectedModel}
              onValueChange={setSelectedModel}
              placeholder={t('Please select a primary model')}
            />
            <p className='text-muted-foreground text-sm'>
              {t('Choose a model that supports the Responses API.')}
            </p>
          </div>
          {model && (
            <>
              <div className='flex flex-col gap-2'>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Merge this configuration into ~/.codex/config.toml, then restart Codex.'
                  )}
                </p>
                <CodeBlock
                  code={config}
                  language='toml'
                  title='config.toml'
                  filename='config.toml'
                  showToolbar
                  enableCollapse={false}
                  className='my-0'
                >
                  <CopyButton
                    value={config}
                    aria-label={t('Copy configuration')}
                  />
                </CodeBlock>
              </div>
              <div className='flex flex-col items-start gap-2'>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Set NEWAPI_API_KEY to this API key in the environment used to start Codex.'
                  )}
                </p>
                <CopyButton
                  value={props.tokenKey}
                  size='sm'
                  variant='outline'
                  aria-label={t('Copy Key')}
                >
                  {t('Copy Key')}
                </CopyButton>
              </div>
            </>
          )}
        </>
      )}
    </Dialog>
  )
}
