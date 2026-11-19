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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { RecordedTurn } from '../api'
import { ConversationTimeline } from '../timeline'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { name: string }) =>
      key.replace('{{name}}', options?.name ?? ''),
  }),
}))
vi.mock('../recorded-turn-body', () => ({
  RecordedTurnBody: ({
    turn,
    previousTurn,
  }: {
    turn: RecordedTurn
    previousTurn?: RecordedTurn
  }) => (
    <div>
      body {turn.trace_id} after {previousTurn?.trace_id ?? 'start'}
    </div>
  ),
}))

function turn(
  trace: string,
  thread: string,
  start: number,
  end: number,
  parent = '',
  name = ''
): RecordedTurn {
  return {
    trace_id: trace,
    request_id: trace,
    conversation_id: thread,
    parent_id: parent,
    agent_name: name,
    started_at_ms: start,
    created_at: end,
    model_name: trace,
  }
}

describe('conversation timeline', () => {
  it('places concurrent agents by start time and expands history from the same thread', async () => {
    const user = userEvent.setup()
    const root = turn('root', 'main', 1000, 9)
    const child = turn('first', 'worker', 2000, 5, 'main', 'reviewer')
    const other = turn('parallel', 'worker2', 3000, 4, 'main', 'tester')
    const followUp = turn('follow-up', 'worker', 4000, 6, 'main', 'reviewer')
    const view = render(
      <ConversationTimeline
        conversationId='main'
        turns={[root, followUp, child, other]}
      />
    )
    const timeline = screen.getByRole('list', { name: 'Conversation timeline' })
    const events = within(timeline).getAllByRole('button')
    expect(events).toHaveLength(4)
    for (const [i, model] of [
      'follow-up',
      'parallel',
      'first',
      'root',
    ].entries()) {
      expect(within(events[i]).getByText(model)).toBeVisible()
    }
    expect(screen.getByText('body follow-up after first')).toBeVisible()
    await user.click(within(timeline).getByRole('button', { name: /tester/ }))
    expect(screen.getByText('body parallel after start')).toBeVisible()
    expect(
      screen.queryByText('body follow-up after first')
    ).not.toBeInTheDocument()
    view.rerender(
      <ConversationTimeline
        conversationId='main'
        turns={[turn('fresh', 'main', 12000, 15), root, followUp, child, other]}
      />
    )
    expect(screen.getByText('body parallel after start')).toBeVisible()
    await user.click(within(timeline).getByRole('button', { name: /tester/ }))
    expect(
      screen.queryByText('body parallel after start')
    ).not.toBeInTheDocument()
  })

  it('opens the recorded parent turn even when a newer main-thread turn intervened', async () => {
    const user = userEvent.setup()
    const parent = { ...turn('dispatch', 'main', 1000, 4), turn_id: 'turn-a' }
    const next = { ...turn('later', 'main', 2000, 5), turn_id: 'turn-b' }
    const child = {
      ...turn('run', 'worker', 3000, 6, 'main', 'reviewer'),
      parent_turn_id: 'turn-a',
    }
    render(
      <ConversationTimeline
        conversationId='main'
        turns={[child, next, parent]}
      />
    )
    await user.click(
      screen.getByRole('button', { name: 'Parent thread: Main thread' })
    )
    expect(screen.getByText('body dispatch after start')).toBeVisible()
    expect(screen.getByRole('button', { name: /dispatch/ })).toHaveFocus()
  })

  it('retains a recorded subagent identity when its parent has expired', () => {
    render(
      <ConversationTimeline
        conversationId='alone'
        turns={[turn('orphan', 'alone', 1000, 2, 'expired', 'research')]}
      />
    )
    expect(screen.getByRole('button', { name: /research/ })).toBeVisible()
    expect(screen.queryByText('Main thread')).not.toBeInTheDocument()
    expect(screen.getByText('Parent thread: expired')).toBeInTheDocument()
  })
})
