import { describe, expect, test } from 'vitest'

import { channelSchema } from '../../types'
import {
  buildSettingJSON,
  channelFormSchema,
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
} from '../channel-form'
import {
  getChannelDisplayBalance,
  normalizeBalanceRechargeMultiplier,
} from '../channel-utils'

describe('channel balance recharge multiplier', () => {
  test('display conversion preserves zero and negative balances and rejects unknown or overflowing results', () => {
    expect(getChannelDisplayBalance(300, 3)).toBe(100)
    expect(getChannelDisplayBalance(0, 3)).toBe(0)
    expect(getChannelDisplayBalance(-9, 3)).toBe(-3)
    expect(getChannelDisplayBalance(null, 3)).toBeNull()
    expect(getChannelDisplayBalance(undefined, 3)).toBeNull()
    expect(getChannelDisplayBalance(Number.POSITIVE_INFINITY, 3)).toBeNull()
    expect(getChannelDisplayBalance(Number.MAX_VALUE, 0.000001)).toBeNull()
  })

  test('absent and invalid legacy multipliers fall back to one without changing the raw balance', () => {
    for (const value of [
      undefined,
      null,
      0,
      -1,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      '3',
      0.0000001,
      1000001,
    ]) {
      expect(normalizeBalanceRechargeMultiplier(value)).toBe(1)
      expect(getChannelDisplayBalance(12, value)).toBe(12)
    }
    expect(normalizeBalanceRechargeMultiplier(0.000001)).toBe(0.000001)
    expect(normalizeBalanceRechargeMultiplier(1000000)).toBe(1000000)
  })

  test('editing and creating channels round-trip the display multiplier separately from upstream cost and raw quota', () => {
    const channel = channelSchema.parse({
      id: 1,
      type: 1,
      name: 'Upstream',
      key: '',
      models: 'test-model',
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 1,
      balance: 300,
      used_quota: 700,
      setting: JSON.stringify({
        balance_recharge_multiplier: 3,
        upstream_cost_ratio: 0.3,
      }),
    })
    const values = transformChannelToFormDefaults(channel)
    expect(values.balance_recharge_multiplier).toBe(3)
    expect(JSON.parse(buildSettingJSON(values))).toMatchObject({
      balance_recharge_multiplier: 3,
      upstream_cost_ratio: 0.3,
    })
    expect(channel.balance).toBe(300)
    expect(channel.used_quota).toBe(700)
    expect(CHANNEL_FORM_DEFAULT_VALUES.balance_recharge_multiplier).toBe(1)
    expect(
      JSON.parse(
        buildSettingJSON({
          ...CHANNEL_FORM_DEFAULT_VALUES,
          balance_recharge_multiplier: 2,
        })
      )
    ).toHaveProperty('balance_recharge_multiplier', 2)
  })

  test('submitted out-of-range multipliers fail validation while an omitted multiplier uses the default', () => {
    const values = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Upstream',
      key: 'example',
      models: 'test-model',
    }
    expect(
      channelFormSchema.safeParse({
        ...values,
        balance_recharge_multiplier: undefined,
      }).success
    ).toBe(true)
    for (const value of [
      0,
      -1,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      0.0000001,
      1000001,
    ]) {
      const result = channelFormSchema.safeParse({
        ...values,
        balance_recharge_multiplier: value,
      })
      expect(result.success).toBe(false)
      if (!result.success) {
        expect(
          result.error.issues.some(
            (issue) => issue.path[0] === 'balance_recharge_multiplier'
          )
        ).toBe(true)
      }
    }
  })
})
