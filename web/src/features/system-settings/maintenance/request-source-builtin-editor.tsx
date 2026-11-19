import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { validSourceName } from './request-source-rules'

export type BuiltinSource = {
  name: string
  examples: Record<string, string>[]
  enabled: boolean
  display_name: string
}

export function BuiltinSourceEditor(props: {
  source: BuiltinSource
  onClose: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const id = useId()
  const schema = z.object({
    enabled: z.boolean(),
    display_name: z
      .string()
      .trim()
      .refine(
        (name) => !name || validSourceName(name),
        t('Enter a valid source name of up to 64 bytes.')
      ),
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: {
      enabled: props.source.enabled,
      display_name:
        props.source.display_name === props.source.name
          ? ''
          : props.source.display_name,
    },
  })
  const update = useMutation({
    mutationFn: async (values: z.output<typeof schema>) => {
      const response = await api.put(
        '/api/option/request_source_rules/builtin',
        { name: props.source.name, ...values }
      )
      requireServerSuccess(response.data)
    },
    onSuccess: () => {
      client.removeQueries({
        queryKey: ['request-source-rules', 'unidentified'],
        type: 'inactive',
      })
      void client.invalidateQueries({
        queryKey: ['request-source-rules', 'builtins'],
      })
      toast.success(t('Setting updated successfully'))
      props.onClose()
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !update.isPending) props.onClose()
      }}
      contentClassName='sm:max-w-md'
      title={t('Edit {{title}}', { title: props.source.name })}
      footer={
        <>
          <Button
            type='button'
            variant='ghost'
            className='sm:mr-auto'
            disabled={update.isPending}
            onClick={() => {
              form.setValue('display_name', '', {
                shouldDirty: true,
                shouldValidate: true,
              })
              form.setValue('enabled', true, { shouldDirty: true })
            }}
          >
            {t('Restore defaults')}
          </Button>
          <Button
            type='button'
            variant='outline'
            disabled={update.isPending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form={id}
            disabled={update.isPending || !form.formState.isDirty}
          >
            {t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={id}
          onSubmit={form.handleSubmit((values) => update.mutate(values))}
          className='space-y-3'
        >
          <FormField
            control={form.control}
            name='display_name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Source display name')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    disabled={update.isPending}
                    placeholder={props.source.name}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchField
                label={t('Enabled')}
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={update.isPending}
              />
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
