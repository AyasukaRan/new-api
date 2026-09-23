// iFlytek Spark batch inference.
// https://www.xfyun.cn/doc/spark/BatchAPI.html
//
// A batch runs for up to 24 hours, so it is modelled as a task: the gateway
// polls it, shows its progress in the task log, and settles it when it lands.
// Billing is deliberately not estimated up front — the real token usage only
// exists in the output file, so the poll fetches that file once the batch
// completes and settles against the model's own rate.
export const meta = {
  apiVersion: 1,
  key: "iflytek-batch",
  name: "iFlytek Batch",
  icon: "text:批",
  description: {
    en: "iFlytek Spark batch inference: submit a JSONL of chat requests and track it to completion",
    zh: "讯飞星火批推理：提交 JSONL 批量对话请求并跟踪任务进度",
  },
  version: "1.4.0",
  author: { name: "QuantumNous" },
  // The same iFlytek channel carries chat and batch. Batch lives on its own
  // host, which the gateway knows, so an operator configures one channel per
  // vendor rather than two.
  channelTypes: [62],
  // This list is the batch allow-list: the host refuses a submit for a model
  // that is not on it, which is the only chance to reject one before a batch is
  // created. It is the set the batch service actually resolves — probed against
  // the live endpoint, which answers every other name, including iFlytek's own
  // MaaS models, with "not found in dx". Listing those would only trade an
  // immediate rejection for a batch that fails an hour later and has to be
  // refunded. An operator whose batch endpoint is a different one points
  // batch_base_url at it and uploads a plugin version with matching models.
  models: ["4.0Ultra", "generalv3.5", "pro-128k"],
  fetchMode: "per_task",
  routes: [
    { method: "POST", path: "/v1/batches", type: "submit", decode: "decodeCreate", render: "renderBatch" },
    { method: "GET", path: "/v1/batches/:task_id", type: "query", render: "renderBatch" },
  ],
};

// The provider accepts exactly one endpoint and one window today. Rejecting
// anything else here keeps the failure at submit time, where the caller can
// still act on it, rather than 24 hours later.
const SUPPORTED_ENDPOINT = "/v1/chat/completions";
const SUPPORTED_WINDOW = "24h";

// usagePhase marks the second poll phase: the batch itself is done and the
// gateway is fetching the output file to learn what it actually cost.
const usagePhase = "usage";

function trimmed(value) {
  return String(value === undefined || value === null ? "" : value).trim();
}

function decodeCreate(ctx) {
  if (!ctx.body || ctx.body.kind !== "json") throw new Error("a JSON body is required");
  const body = ctx.body.value;
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("the request body must be an object");

  const inputFileId = trimmed(body.input_file_id);
  if (!inputFileId) throw new Error("input_file_id is required");

  const endpoint = trimmed(body.endpoint) || SUPPORTED_ENDPOINT;
  if (endpoint !== SUPPORTED_ENDPOINT) throw new Error('endpoint must be "' + SUPPORTED_ENDPOINT + '"');

  const window = trimmed(body.completion_window) || SUPPORTED_WINDOW;
  if (window !== SUPPORTED_WINDOW) throw new Error('completion_window must be "' + SUPPORTED_WINDOW + '"');

  const metadata = body.metadata;
  if (metadata !== undefined && (!metadata || typeof metadata !== "object" || Array.isArray(metadata))) {
    throw new Error("metadata must be an object");
  }

  // The model lives inside the uploaded file, which this hook cannot read, so
  // the caller states it here. The gateway checks the claim against the file it
  // recorded at upload before the request leaves, so a mismatch cannot be used
  // to have an expensive model billed at a cheap one's rate.
  const model = trimmed(body.model) || trimmed(metadata && metadata.model);
  if (!model) throw new Error('model is required: name the same model the uploaded file uses, e.g. {"model": "4.0Ultra"}');

  return {
    kind: "submit",
    model: model,
    requestBody: { input_file_id: inputFileId, endpoint: endpoint, completion_window: window, metadata: metadata },
  };
}

export function buildSubmitRequest(ctx) {
  const body = ctx.requestBody || {};
  const payload = {
    input_file_id: body.input_file_id,
    endpoint: body.endpoint || SUPPORTED_ENDPOINT,
    completion_window: body.completion_window || SUPPORTED_WINDOW,
  };
  if (body.metadata) payload.metadata = body.metadata;
  return {
    url: ctx.baseUrl + "/v1/batches",
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: payload,
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body;
  if (!body || typeof body !== "object") throw new Error("the provider returned an unreadable response");
  if (body.error) throw new Error(trimmed(body.error.message) || "batch creation failed");
  const batchId = trimmed(body.id);
  if (!batchId) throw new Error("the provider returned no batch id");
  return { taskId: batchId, taskData: body };
}

export function buildQueryRequest(ctx) {
  const state = ctx.state || {};
  if (state.phase === usagePhase && trimmed(state.outputFileId)) {
    return {
      url: ctx.baseUrl + "/v1/files/" + trimmed(state.outputFileId) + "/content",
      method: "GET",
      headers: { Authorization: "Bearer " + ctx.apiKey },
    };
  }
  return {
    url: ctx.baseUrl + "/v1/batches/" + ctx.taskId,
    method: "GET",
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

// The provider's own status vocabulary. Anything outside it is reported as
// UNKNOWN so the host counts a poll failure instead of inventing progress.
const STATUS_BY_PROVIDER_STATE = {
  validating: "SUBMITTED",
  queuing: "QUEUED",
  in_progress: "IN_PROGRESS",
  finalizing: "IN_PROGRESS",
  completed: "SUCCESS",
  failed: "FAILURE",
  expired: "FAILURE",
  cancelling: "IN_PROGRESS",
  cancelled: "FAILURE",
  canceled: "FAILURE",
};

function batchProgress(counts) {
  if (!counts || typeof counts !== "object") return "";
  const total = Number(counts.total);
  if (!Number.isFinite(total) || total <= 0) return "";
  const done = Number(counts.completed || 0) + Number(counts.failed || 0);
  if (!Number.isFinite(done) || done < 0) return "";
  return Math.min(100, Math.floor((done / total) * 100)) + "%";
}

function failureReason(body) {
  const errors = body && body.errors;
  const list = errors && Array.isArray(errors.data) ? errors.data : [];
  const messages = list
    .map(function (item) {
      return trimmed(item && item.message);
    })
    .filter(Boolean);
  if (messages.length) return messages.join("; ");
  return trimmed(body && body.status) || "batch failed";
}

function parseBatchStatus(ctx, body) {
  if (!body || typeof body !== "object") return { status: "UNKNOWN" };
  const providerStatus = trimmed(body.status).toLowerCase();
  const status = STATUS_BY_PROVIDER_STATE[providerStatus];
  if (!status) return { status: "UNKNOWN" };

  if (status === "FAILURE") {
    return { status: "FAILURE", reason: failureReason(body), progress: batchProgress(body.request_counts) };
  }
  if (status !== "SUCCESS") {
    return { status: status, progress: batchProgress(body.request_counts) };
  }

  const counts = (body.request_counts && typeof body.request_counts === "object" && body.request_counts) || {};
  const completedCount = Number(counts.completed || 0);
  const failedCount = Number(counts.failed || 0);
  const outputFileId = trimmed(body.output_file_id);

  if (!outputFileId && completedCount <= 0 && failedCount > 0) {
    // The provider calls this "completed", but not one request produced a
    // result. Reporting success would leave the estimate charged for output
    // nobody received; a failure runs the host's refund instead.
    const errorFileId = trimmed(body.error_file_id);
    let reason = "every request in the batch failed (" + failedCount + " of " + (Number(counts.total || failedCount) || failedCount) + ")";
    if (errorFileId) reason += "; see the error file artifact for the per-line cause";
    return { status: "FAILURE", reason: reason, progress: "100%" };
  }

  if (!outputFileId) {
    // Completed with nothing to read back: there is no usage to settle, so the
    // task lands on the estimate rather than waiting for a file that will
    // never arrive.
    return { status: "SUCCESS", progress: "100%" };
  }
  // Hold the task just short of terminal for one more round. The next poll
  // fetches the output file, which is the only place the real usage exists.
  return {
    status: "IN_PROGRESS",
    progress: "99%",
    state: { phase: usagePhase, outputFileId: outputFileId, batch: body },
  };
}

// Each output line wraps one chat completion. Only the usage block matters
// here; the deliverable itself stays in the provider's file.
function sumUsage(body) {
  const lines = typeof body === "string" ? body.split("\n") : [];
  const records = lines.length ? lines : [body];
  let totalTokens = 0;
  let completionTokens = 0;
  for (const record of records) {
    let parsed = record;
    if (typeof record === "string") {
      const text = record.trim();
      if (!text) continue;
      try {
        parsed = JSON.parse(text);
      } catch (_) {
        continue;
      }
    }
    if (!parsed || typeof parsed !== "object") continue;
    const usage = (parsed.response && parsed.response.body && parsed.response.body.usage) || parsed.usage;
    if (!usage || typeof usage !== "object") continue;
    const total = Number(usage.total_tokens);
    const completion = Number(usage.completion_tokens);
    if (Number.isFinite(total) && total > 0) totalTokens += total;
    if (Number.isFinite(completion) && completion > 0) completionTokens += completion;
  }
  return { totalTokens: totalTokens, completionTokens: completionTokens };
}

export function parseTaskResult(ctx, body, response) {
  const state = (ctx && ctx.state) || {};
  if (state.phase !== usagePhase) return parseBatchStatus(ctx, body);

  const usage = sumUsage(body);
  const result = {
    status: "SUCCESS",
    progress: "100%",
    // Keep the batch object as the task's data: it is what the caller reads
    // back, and the output file is fetched from the provider on demand.
    data: state.batch,
    state: { phase: "settled", outputFileId: state.outputFileId },
  };
  if (usage.totalTokens > 0) result.totalTokens = usage.totalTokens;
  if (usage.completionTokens > 0) result.completionTokens = usage.completionTokens;
  if (response && response.status >= 400) {
    // The batch itself succeeded; only the usage read failed. Landing the task
    // on its estimate is strictly better than failing (and refunding) work the
    // provider already did.
    return { status: "SUCCESS", progress: "100%", data: state.batch, state: { phase: "settled" } };
  }
  return result;
}

// The provider's batch envelope, rebuilt from what the gateway persisted so a
// caller can poll the gateway exactly as it would poll the provider.
function batchView(task) {
  const data = task && task.data && typeof task.data === "object" ? task.data : {};
  return {
    id: trimmed(task && task.task_id),
    object: "batch",
    endpoint: trimmed(data.endpoint) || SUPPORTED_ENDPOINT,
    input_file_id: trimmed(data.input_file_id),
    completion_window: trimmed(data.completion_window) || SUPPORTED_WINDOW,
    status: trimmed(data.status),
    output_file_id: trimmed(data.output_file_id),
    error_file_id: trimmed(data.error_file_id),
    created_at: Number(data.created_at || task.created_at || 0),
    request_counts: data.request_counts || { total: 0, completed: 0, failed: 0 },
    metadata: data.metadata || null,
    // Gateway-side lifecycle, so a caller polling here can tell a batch that is
    // still running from one the gateway has finished settling.
    gateway_status: trimmed(task && task.status),
    gateway_progress: trimmed(task && task.progress),
    gateway_fail_reason: trimmed(task && task.fail_reason),
  };
}

// The deliverable of a batch is a file the provider holds. Publishing it as a
// task artifact is what makes it reachable: a caller cannot ask for it through
// /v1/files, which only resolves ids the gateway itself issued at upload.
const ARTIFACT_OUTPUT = "output";
const ARTIFACT_ERRORS = "errors";

function artifactFileId(data, artifactKey) {
  const batch = data && typeof data === "object" ? data : {};
  if (artifactKey === ARTIFACT_OUTPUT) return trimmed(batch.output_file_id);
  if (artifactKey === ARTIFACT_ERRORS) return trimmed(batch.error_file_id);
  return "";
}

export function listArtifacts(task) {
  const status = trimmed(task && task.status).toUpperCase();
  if (status !== "SUCCESS" && status !== "FAILURE") return [];
  const artifacts = [];
  for (const key of [ARTIFACT_OUTPUT, ARTIFACT_ERRORS]) {
    if (artifactFileId(task && task.data, key)) {
      artifacts.push({ key: key, type: "file", mimeType: "application/x-ndjson" });
    }
  }
  return artifacts;
}

export function buildContentRequest(ctx) {
  const fileId = artifactFileId(ctx && ctx.data, ctx && ctx.artifactKey);
  if (!fileId) throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl + "/v1/files/" + fileId + "/content",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export const native = {
  decodeCreate: decodeCreate,
  renderBatch: function (ctx, task) {
    return batchView(task);
  },
  error: function (ctx, error) {
    return { error: { code: error.code, message: error.message, type: "invalid_request_error" } };
  },
};
