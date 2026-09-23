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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { t } from 'i18next'
import { useCallback, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { getChannels } from '@/features/channels/api'
import { CHANNEL_STATUS_LABELS } from '@/features/channels/constants'
import type { Channel } from '@/features/channels/types'

import { getSystemOptions, updateSystemOption } from '../api'
import {
  channelPricingEditorData,
  channelPricingFromDraft,
  channelPricingSnapshot,
  isPricingRecord,
  modelChannelOverrides,
  type ChannelPricing,
  type ChannelPricingMap,
} from './model-channel-pricing'
import type { ModelRatioData } from './model-pricing-core'

const OPTION_KEY = 'ChannelModelPricing'
const EMPTY_OVERRIDES: Record<string, ChannelPricing> = {}
type ChannelOption = { value: string; label: string }
type PricingMutation = {
  modelName: string
  channelId: string
  baseline: ChannelPricing | undefined
  draft: ModelRatioData | null
  initialData?: ModelRatioData
}

class ChannelPricingConflict extends Error {}

async function readChannelPricing(
  modelName: string
): Promise<ChannelPricingMap> {
  const response = await getSystemOptions()
  if (!response.success) {
    throw new Error(response.message || t('Failed to load channel pricing'))
  }
  if (!Array.isArray(response.data)) {
    throw new Error(t('Invalid channel pricing settings.'))
  }
  const raw = response.data.find((option) => option.key === OPTION_KEY)?.value
  let map: unknown
  try {
    map = raw ? JSON.parse(raw) : {}
  } catch {
    throw new Error(t('Invalid channel pricing settings.'))
  }
  if (!isPricingRecord(map)) {
    throw new Error(t('Invalid channel pricing settings.'))
  }
  modelChannelOverrides(map, modelName)
  return map
}

async function readPricingChannels(): Promise<
  Pick<Channel, 'id' | 'name' | 'status' | 'models'>[]
> {
  const channels: Pick<Channel, 'id' | 'name' | 'status' | 'models'>[] = []
  let page = 1
  let total = 0
  do {
    const response = await getChannels({
      p: page,
      page_size: 100,
      id_sort: true,
    })
    if (
      !response.success ||
      !response.data ||
      !Array.isArray(response.data.items)
    ) {
      throw new Error(response.message || t('Failed to load channels'))
    }
    total = response.data.total
    if (
      !Number.isSafeInteger(total) ||
      total < 0 ||
      (response.data.items.length === 0 && channels.length < total)
    ) {
      throw new Error(t('Failed to load channels'))
    }
    channels.push(
      ...response.data.items.map(({ id, name, status, models }) => ({
        id,
        name,
        status,
        models,
      }))
    )
    page += 1
  } while (channels.length < total)
  return channels
}

export function useModelChannelPricing(modelName?: string) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const saving = useRef(false)
  const [reloading, setReloading] = useState(false)
  const [failure, setFailure] = useState({
    modelName,
    message: '',
    conflict: false,
  })
  const pricingQuery = useQuery({
    queryKey: ['model-channel-pricing', modelName],
    queryFn: () => readChannelPricing(modelName ?? ''),
    enabled: Boolean(modelName),
    retry: false,
    // Keep the editing baseline fixed until reload; reopening starts a fresh read.
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const channelsQuery = useQuery({
    queryKey: ['channels', 'pricing-override-picker'],
    queryFn: readPricingChannels,
    enabled: Boolean(modelName),
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  })
  const overrides = useMemo(() => {
    if (!modelName || !pricingQuery.data) return EMPTY_OVERRIDES
    return modelChannelOverrides(pricingQuery.data, modelName)
  }, [modelName, pricingQuery.data])

  const channelOptions = useMemo(() => {
    const options = new Map<string, ChannelOption>()
    for (const channel of channelsQuery.data ?? []) {
      if (!channel.models?.split(',').includes(modelName ?? '')) continue
      const status =
        CHANNEL_STATUS_LABELS[
          channel.status as keyof typeof CHANNEL_STATUS_LABELS
        ] ?? 'Unknown'
      options.set(String(channel.id), {
        value: String(channel.id),
        label: `#${channel.id} ${channel.name} · ${t(status)}`,
      })
    }
    for (const channelId of Object.keys(overrides)) {
      if (options.has(channelId)) continue
      const channel = channelsQuery.data?.find(
        (item) => String(item.id) === channelId
      )
      options.set(channelId, {
        value: channelId,
        label: `#${channelId} ${channel?.name ?? ''} · ${t('Unavailable channel')}`,
      })
    }
    return [...options.values()]
  }, [channelsQuery.data, modelName, overrides, t])

  const getEditorData = useCallback(
    (channelId: string, globalPricing: ModelRatioData) =>
      channelPricingEditorData(
        globalPricing,
        Object.hasOwn(overrides, channelId) ? overrides[channelId] : undefined
      ),
    [overrides]
  )
  const hasOverride = useCallback(
    (channelId: string) => Object.hasOwn(overrides, channelId),
    [overrides]
  )

  const mutation = useMutation({
    mutationFn: async (request: PricingMutation) => {
      if (
        !/^\d+$/.test(request.channelId) ||
        !Number.isSafeInteger(Number(request.channelId)) ||
        Number(request.channelId) <= 0
      ) {
        throw new Error(t('Choose a valid channel for every override.'))
      }
      const next = request.draft
        ? channelPricingFromDraft(
            request.draft,
            request.baseline,
            request.initialData
          )
        : null
      const latest = await readChannelPricing(request.modelName)
      const currentModel = modelChannelOverrides(latest, request.modelName)
      const current = Object.hasOwn(currentModel, request.channelId)
        ? currentModel[request.channelId]
        : undefined
      if (
        channelPricingSnapshot(current) !==
        channelPricingSnapshot(request.baseline)
      ) {
        throw new ChannelPricingConflict(
          t(
            'Channel pricing changed elsewhere. Reload before saving to avoid overwriting it.'
          )
        )
      }
      const nextModel = { ...currentModel }
      if (next) nextModel[request.channelId] = next
      else delete nextModel[request.channelId]
      const result = { ...latest, [request.modelName]: nextModel }
      if (Object.keys(nextModel).length === 0) delete result[request.modelName]
      const response = await updateSystemOption({
        key: OPTION_KEY,
        value: JSON.stringify(result),
      })
      if (!response.success) {
        throw new Error(response.message || t('Failed to save channel pricing'))
      }
      return result
    },
    onSuccess: (map, request) => {
      void client.invalidateQueries({
        queryKey: ['model-channel-pricing'],
        refetchType: 'none',
      })
      client.setQueryData(['model-channel-pricing', request.modelName], map)
      void client.invalidateQueries({ queryKey: ['system-options'] })
      void client.invalidateQueries({ queryKey: ['pricing'] })
      toast.success(t('Setting updated successfully'))
    },
  })

  const commit = async (
    channelId: string,
    draft: ModelRatioData | null,
    initialData?: ModelRatioData
  ): Promise<boolean> => {
    if (
      saving.current ||
      !modelName ||
      !pricingQuery.data ||
      channelsQuery.isPending ||
      pricingQuery.isError ||
      channelsQuery.isError
    ) {
      return false
    }
    saving.current = true
    setFailure({ modelName, message: '', conflict: false })
    try {
      await mutation.mutateAsync({
        modelName,
        channelId,
        draft,
        initialData,
        baseline: Object.hasOwn(overrides, channelId)
          ? overrides[channelId]
          : undefined,
      })
      return true
    } catch (error) {
      setFailure({
        modelName,
        message:
          error instanceof Error
            ? error.message
            : t('Failed to save channel pricing'),
        conflict: error instanceof ChannelPricingConflict,
      })
      return false
    } finally {
      saving.current = false
    }
  }

  const reload = async (): Promise<boolean> => {
    if (!modelName || saving.current) return false
    saving.current = true
    setReloading(true)
    try {
      const [pricing, channels] = await Promise.all([
        readChannelPricing(modelName),
        readPricingChannels(),
      ])
      client.setQueryData(['model-channel-pricing', modelName], pricing)
      client.setQueryData(['channels', 'pricing-override-picker'], channels)
      setFailure({ modelName, message: '', conflict: false })
      return true
    } catch (error) {
      setFailure({
        modelName,
        message:
          error instanceof Error
            ? error.message
            : t('Failed to load channel pricing'),
        conflict: failure.modelName === modelName && failure.conflict,
      })
      return false
    } finally {
      saving.current = false
      setReloading(false)
    }
  }
  const loadError =
    pricingQuery.error?.message || channelsQuery.error?.message || ''

  return {
    channelOptions,
    isLoading:
      Boolean(modelName) && (pricingQuery.isPending || channelsQuery.isPending),
    loadError,
    saveError: failure.modelName === modelName ? failure.message : '',
    conflict: failure.modelName === modelName && failure.conflict,
    isSaving: mutation.isPending || reloading,
    getEditorData,
    hasOverride,
    save: (
      channelId: string,
      draft: ModelRatioData,
      initialData?: ModelRatioData
    ) => commit(channelId, draft, initialData),
    reset: (channelId: string) => commit(channelId, null),
    reload,
  }
}
