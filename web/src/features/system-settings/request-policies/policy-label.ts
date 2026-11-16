// Labels for the decision events recorded by the backend request policy state
// (reasons, sources and health), the session behaviors, and the auto-disable
// sources reported by the channel operations summary.
export function policyLabel(
  t: (key: string) => string,
  value: string,
  unknownLabel = value
): string {
  switch (value) {
    case 'upstream_failure':
      return t('Upstream request failed')
    case 'request_failed':
      return t('Request failed')
    case 'task_failed':
      return t('Task failed')
    case 'same_key_transient_retry':
      return t('Retry the same key once after a temporary upstream error')
    case 'response_already_started':
      return t(
        'Response content has already been sent; retrying could duplicate output'
      )
    case 'client_canceled':
      return t('The client disconnected; retrying would no longer help')
    case 'skip_retry_flag':
      return t('Retry is disabled for this error')
    case 'invalid_channel_credential':
      return t('The current credential was rejected; try another key')
    case 'channel_credentials_exhausted':
      return t('No usable credentials remain for this request')
    case 'channel_endpoint_unavailable':
      return t('The current endpoint is unavailable; try another address')
    case 'strict_affinity_rule':
    case 'strict_session':
      return t('Session behavior prevents switching channels')
    case 'attempt_budget_exhausted':
      return t('Total attempt budget exhausted')
    case 'channel_error':
      return t('Channel error')
    case 'unrecognized_status':
      return t('Unrecognized status code')
    case 'task_accepted':
      return t('The task was accepted; submission cannot be replayed')
    case 'pinned_channel_constraint':
    case 'pinned_channel':
      return t('Fixed channel constraint')
    case 'local_rejection':
      return t('Request rejected locally')
    case 'non_retryable_error':
    case 'system_retry_exclusion':
      return t('System retry exclusion')
    case 'status_not_retryable':
      return t('Status is outside the retry rules')
    case 'retry_status_matched':
      return t('Status matches the retry rules')
    case 'session_rule_matched':
      return t('Session rule matched')
    case 'channel_selected':
      return t('Channel selected')
    case 'request_completed':
      return t('Request completed')
    case 'stream_not_successful':
      return t('Stream did not complete successfully')
    case 'channel':
      return t('Channel')
    case 'global':
      return t('Global default')
    case 'global_and_channel':
      return t('Global and channel settings')
    case 'session_rule':
      return t('Session rule')
    case 'system':
      return t('System constraint')
    case 'routing':
      return t('Channel selection')
    case 'upstream':
      return t('Upstream response')
    case 'transport':
      return t('Upstream connection')
    case 'local':
      return t('Local check')
    case 'channel_constraint':
      return t('Fixed channel constraint')
    case 'unchanged':
      return t('Unchanged')
    case 'channel_disable_requested':
      return t('Channel disable requested')
    case 'key_disable_requested':
      return t('Current key disable requested')
    case 'off':
    case '':
      return t('Do not keep sessions')
    case 'prefer':
      return t('Prefer the original channel, allow switching')
    case 'strict':
      return t('Require the original channel')
    default:
      return unknownLabel
  }
}
