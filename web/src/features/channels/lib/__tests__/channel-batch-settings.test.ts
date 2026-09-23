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
import { describe, expect, it } from 'vitest'

import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  type ChannelFormValues,
} from '../channel-form'

function formValues(overrides: Partial<ChannelFormValues>): ChannelFormValues {
  return { ...CHANNEL_FORM_DEFAULT_VALUES, ...overrides } as ChannelFormValues
}

describe('channel batch settings', () => {
  it('a channel that never touched batch writes no batch keys', () => {
    // The settings blob is rewritten wholesale on save, so writing defaults
    // here would flip every existing channel into a batch candidate.
    const settings = JSON.parse(buildSettingJSON(formValues({})))
    expect(settings).not.toHaveProperty('batch_enabled')
    expect(settings).not.toHaveProperty('batch_base_url')
    expect(settings).not.toHaveProperty('batch_key')
  })

  it('enabling batch alone records the opt-in and nothing else', () => {
    const settings = JSON.parse(
      buildSettingJSON(formValues({ batch_enabled: true }))
    )
    expect(settings.batch_enabled).toBe(true)
    expect(settings).not.toHaveProperty('batch_base_url')
    expect(settings).not.toHaveProperty('batch_key')
  })

  it('a separate batch host and credential are both persisted', () => {
    const settings = JSON.parse(
      buildSettingJSON(
        formValues({
          batch_enabled: true,
          batch_base_url: 'https://spark-api-open.xf-yun.com/',
          batch_key: '  secret  ',
        })
      )
    )
    expect(settings.batch_enabled).toBe(true)
    expect(settings.batch_base_url).toBe('https://spark-api-open.xf-yun.com')
    expect(settings.batch_key).toBe('secret')
  })

  it('blank overrides are dropped rather than saved as empty strings', () => {
    // An empty override must fall back to the channel's own endpoint, which it
    // cannot do if the blank is persisted.
    const settings = JSON.parse(
      buildSettingJSON(
        formValues({
          batch_enabled: true,
          batch_base_url: '   ',
          batch_key: '   ',
        })
      )
    )
    expect(settings).not.toHaveProperty('batch_base_url')
    expect(settings).not.toHaveProperty('batch_key')
  })
})
