package keyusage

// Window totals are updated with every state transition. History is separate:
// admission only visits candidates and newly expired events, never 30 days of
// traffic. Cleanup yields after bounded batches so it cannot monopolize Redis.
const ledgerLua = `
local op = ARGV[1]
local args = cjson.decode(ARGV[2])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local window_start = now - 86400000
local history_start = now - 2592000000
local lease_duration = 900000
local fields = {'i', 'o', 'c', 'r', 'a', 'e', 'u', 'p', 'pi', 'po', 'pc', 'pk'}

local function result(value)
  return cjson.encode(value)
end

local function read_event(id)
  local raw = redis.call('HGET', KEYS[2], id)
  if raw then return cjson.decode(raw) end
end

local function read_total(identity)
  local raw = redis.call('HGET', KEYS[5], identity)
  local total = raw and cjson.decode(raw) or {}
  for _, field in ipairs(fields) do total[field] = tonumber(total[field]) or 0 end
  return total
end

local function contribution(event)
  local value = {i=0, o=0, c=0, r=1, a=0, e=0, u=0, p=0, pi=0, po=0, pc=0, pk=0}
  if event.status == 'pending' then
    value.p, value.pi, value.po = 1, event.input_tokens, event.output_tokens
    if type(event.cost_usd) == 'number' then value.pc, value.pk = event.cost_usd, 1 end
  else
    value.i, value.o = event.input_tokens, event.output_tokens
    if type(event.cost_usd) == 'number' then
      value.c = event.cost_usd
      if event.cost_actual then value.a = 1 else value.e = 1 end
    else
      value.u = 1
    end
  end
  return value
end

local function change_total(before, after)
  local identity = after and after.key_identity or before.key_identity
  local total = read_total(identity)
  local old = before and contribution(before)
  local new = after and contribution(after)
  for _, field in ipairs(fields) do
    local value = total[field] - (old and old[field] or 0) + (new and new[field] or 0)
    if value > 9007199254740991 then return false end
    total[field] = math.max(0, value)
  end
  if total.r == 0 then
    redis.call('HDEL', KEYS[5], identity)
  else
    -- String values preserve integer precision beyond cjson's 14-digit encoder.
    for _, field in ipairs(fields) do
      local format = (field == 'c' or field == 'pc') and '%.17g' or '%.0f'
      total[field] = string.format(format, total[field])
    end
    redis.call('HSET', KEYS[5], identity, cjson.encode(total))
  end
  return true
end

local function save_event(event)
  redis.call('HSET', KEYS[2], event.id, cjson.encode(event))
end

local function release(event)
  event.status, event.input_tokens, event.output_tokens = 'released', 0, 0
  event.tokens_estimated = false
  if not event.cost_actual then event.cost_usd = cjson.null end
end

local function touch()
  for _, key in ipairs(KEYS) do redis.call('EXPIRE', key, 2678400) end
end

local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', window_start, 'LIMIT', 0, 256)
for _, id in ipairs(expired) do
  local event = read_event(id)
  if event then change_total(event, nil) end
  redis.call('ZREM', KEYS[3], id)
end

expired = redis.call('ZRANGEBYSCORE', KEYS[4], '-inf', now, 'LIMIT', 0, 256)
for _, id in ipairs(expired) do
  local before = read_event(id)
  if before and before.status == 'pending' then
    local after = cjson.decode(cjson.encode(before))
    release(after)
    if redis.call('ZSCORE', KEYS[3], id) then change_total(before, after) end
    save_event(after)
  end
  redis.call('ZREM', KEYS[4], id)
end

expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', history_start, 'LIMIT', 0, 256)
for _, id in ipairs(expired) do
  if redis.call('ZSCORE', KEYS[3], id) then
    local event = read_event(id)
    if event then change_total(event, nil) end
    redis.call('ZREM', KEYS[3], id)
  end
  redis.call('ZREM', KEYS[4], id)
  redis.call('HDEL', KEYS[2], id)
  redis.call('ZREM', KEYS[1], id)
end

if redis.call('ZCOUNT', KEYS[3], '-inf', window_start) > 0 or
   redis.call('ZCOUNT', KEYS[4], '-inf', now) > 0 or
   redis.call('ZCOUNT', KEYS[1], '-inf', history_start) > 0 then
  return result({again=true})
end

if op == 'query' then
  local first = args.start == 0
  local from = first and (now - args.hours * 3600000) or args.start
  local ending = first and now or args['end']
  local minimum = args.cursor > 0 and tostring(args.cursor) or '(' .. string.format('%.0f', from)
  local ids = redis.call('ZRANGEBYSCORE', KEYS[1], minimum, ending, 'LIMIT', args.offset, 512)
  local items = {}
  for _, id in ipairs(ids) do
    local raw = redis.call('HGET', KEYS[2], id)
    if raw then table.insert(items, raw) end
  end
  local reply = {window_start=from, window_end=ending}
  local first_event = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
  if #first_event > 0 then reply.observed_since = tonumber(first_event[2]) end
  if #items > 0 then reply.items = items end
  return result(reply)
end

if op == 'reserve' then
  local existing = read_event(args.id)
  if existing then
    for _, candidate in ipairs(args.candidates) do
      if candidate.identity == existing.key_identity and (args.preferred == '' or args.preferred == existing.key_identity) then
        return result({event=existing})
      end
    end
    return result({error='conflict'})
  end
  if redis.call('ZCARD', KEYS[1]) >= 5000000 or redis.call('ZCARD', KEYS[4]) >= 100000 then
    return result({error='limit'})
  end
  local totals, token_sum, cost_sum, weight_sum = {}, 0, 0, 0
  local comparable_cost = true
  for index, candidate in ipairs(args.candidates) do
    local total = read_total(candidate.identity)
    local tokens = total.i + total.o + total.pi + total.po
    local cost = total.c + total.pc
    totals[index] = {tokens=tokens, cost=cost}
    token_sum, cost_sum, weight_sum = token_sum + tokens, cost_sum + cost, weight_sum + candidate.weight
    if type(candidate.cost_usd) ~= 'number' or total.u > 0 or total.pk < total.p then comparable_cost = false end
  end
  local token_square, cost_square = 0, 0
  for index, candidate in ipairs(args.candidates) do
    local share = candidate.weight / weight_sum
    token_square = token_square + totals[index].tokens * totals[index].tokens / share
    cost_square = cost_square + totals[index].cost * totals[index].cost / share
  end
  local cursor = tonumber(redis.call('HGET', KEYS[5], '@cursor') or '0')
  local best, best_score
  for offset = 1, #args.candidates do
    local index = (cursor + offset - 1) % #args.candidates + 1
    local candidate, total = args.candidates[index], totals[index]
    if args.preferred == '' or args.preferred == candidate.identity then
      local share = candidate.weight / weight_sum
      local tokens = candidate.input_tokens + candidate.output_tokens
      local score = 0
      if token_sum + tokens > 0 then
        score = (token_square + (2 * total.tokens * tokens + tokens * tokens) / share) / ((token_sum + tokens) * (token_sum + tokens)) - 1
      end
      if comparable_cost and cost_sum + candidate.cost_usd > 0 then
        local cost_score = (cost_square + (2 * total.cost * candidate.cost_usd + candidate.cost_usd * candidate.cost_usd) / share) / ((cost_sum + candidate.cost_usd) * (cost_sum + candidate.cost_usd)) - 1
        score = (score + cost_score) / 2
      end
      if best == nil or score < best_score then best, best_score = index, score end
    end
  end
  if best == nil then return result({error='conflict'}) end
  local candidate = args.candidates[best]
  local event = {id=args.id, key_identity=candidate.identity, started_at=now,
    input_tokens=candidate.input_tokens, output_tokens=candidate.output_tokens,
    cost_usd=candidate.cost_usd, cost_actual=false, tokens_estimated=true, status='pending'}
  if not change_total(nil, event) then return result({error='limit'}) end
  save_event(event)
  redis.call('ZADD', KEYS[1], now, event.id)
  redis.call('ZADD', KEYS[3], now, event.id)
  redis.call('ZADD', KEYS[4], now + lease_duration, event.id)
  redis.call('HSET', KEYS[5], '@cursor', best % #args.candidates)
  touch()
  return result({event=event})
end

local before = read_event(args.id)
if not before then return result({error='not_found'}) end
if op ~= 'correct' and (args.identity ~= before.key_identity or args.started_at ~= math.floor(before.started_at / 1000)) then
  return result({error='conflict'})
end
if op == 'renew' then
  if before.status ~= 'pending' then return result({error='conflict'}) end
  redis.call('ZADD', KEYS[4], now + lease_duration, before.id)
  touch()
  return result({ok=true})
end
local after = cjson.decode(cjson.encode(before))
if op == 'correct' then
  after.cost_usd, after.cost_actual = args.cost_usd, true
elseif op == 'finish' then
  if before.status == 'finished' then return result({ok=true}) end
  if type(args.observation) == 'table' then
    local usage = args.observation
    after.status, after.input_tokens, after.output_tokens = 'finished', usage.input_tokens, usage.output_tokens
    after.tokens_estimated = usage.tokens_estimated
    if not before.cost_actual then after.cost_usd, after.cost_actual = usage.cost_usd, usage.cost_actual end
  else
    release(after)
  end
else
  return result({error='invalid_operation'})
end
if redis.call('ZSCORE', KEYS[3], before.id) and not change_total(before, after) then return result({error='limit'}) end
save_event(after)
if op == 'finish' then redis.call('ZREM', KEYS[4], before.id) end
touch()
return result({ok=true})
`
