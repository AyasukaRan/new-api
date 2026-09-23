package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadBatchPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("iflytek-batch")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "iflytek-batch"})
	require.NoError(t, err)
	return plugin
}

func decodeCreate(t *testing.T, plugin *jsplugin.LoadedPlugin, args ...any) (any, error) {
	t.Helper()
	return plugin.Engine.CallPath(t.Context(), "native", []string{"decodeCreate"}, args...)
}

func callBatchHook(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) map[string]any {
	t.Helper()
	var value any
	var err error
	if hook == "native.decodeCreate" {
		value, err = decodeCreate(t, plugin, args...)
	} else {
		value, err = plugin.Engine.Call(t.Context(), hook, args...)
	}
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func TestBatchSubmitRejectsRequestsTheProviderCannotServe(t *testing.T) {
	plugin := loadBatchPlugin(t)
	jsonBody := func(value map[string]any) map[string]any {
		return map[string]any{"body": map[string]any{"kind": "json", "value": value}}
	}

	t.Run("a well formed request carries the model and file through", func(t *testing.T) {
		decoded := callBatchHook(t, plugin, "native.decodeCreate", jsonBody(map[string]any{
			"input_file_id": "file-abc", "model": "4.0Ultra", "metadata": map[string]any{"note": "nightly"},
		}))
		assert.Equal(t, "submit", decoded["kind"])
		assert.Equal(t, "4.0Ultra", decoded["model"])
		body, ok := decoded["requestBody"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "file-abc", body["input_file_id"])
		assert.Equal(t, "/v1/chat/completions", body["endpoint"])
		assert.Equal(t, "24h", body["completion_window"])
	})

	// Each of these is rejected at submit rather than 24 hours later, which is
	// when the provider would otherwise surface it.
	rejected := []struct {
		name string
		body map[string]any
	}{
		{"a missing input file", map[string]any{"model": "4.0Ultra"}},
		{"a missing model", map[string]any{"input_file_id": "file-abc"}},
		{"an unsupported endpoint", map[string]any{"input_file_id": "file-abc", "model": "4.0Ultra", "endpoint": "/v1/embeddings"}},
		{"an unsupported window", map[string]any{"input_file_id": "file-abc", "model": "4.0Ultra", "completion_window": "7d"}},
	}
	for _, testCase := range rejected {
		t.Run(testCase.name+" is refused", func(t *testing.T) {
			_, err := decodeCreate(t, plugin, jsonBody(testCase.body))
			require.Error(t, err)
		})
	}
}

func TestBatchPollingHoldsCompletionUntilRealUsageIsRead(t *testing.T) {
	plugin := loadBatchPlugin(t)
	queryContext := func(state any) map[string]any {
		return map[string]any{"taskId": "batch-1", "baseUrl": "https://provider", "apiKey": "secret", "state": state}
	}

	t.Run("a running batch reports how far it has got", func(t *testing.T) {
		result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil), map[string]any{
			"status": "in_progress", "request_counts": map[string]any{"total": 10, "completed": 4, "failed": 1},
		}, map[string]any{"status": 200})
		assert.Equal(t, "IN_PROGRESS", result["status"])
		assert.Equal(t, "50%", result["progress"])
	})

	t.Run("a completed batch is held back for one more round", func(t *testing.T) {
		result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil), map[string]any{
			"status": "completed", "output_file_id": "file-out", "request_counts": map[string]any{"total": 2, "completed": 2, "failed": 0},
		}, map[string]any{"status": 200})
		// Terminal SUCCESS here would settle the task before anyone has read
		// what it actually cost.
		assert.Equal(t, "IN_PROGRESS", result["status"])
		state, ok := result["state"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "usage", state["phase"])
		assert.Equal(t, "file-out", state["outputFileId"])
	})

	t.Run("the usage round fetches the output file", func(t *testing.T) {
		descriptor := callBatchHook(t, plugin, "buildQueryRequest",
			queryContext(map[string]any{"phase": "usage", "outputFileId": "file-out"}))
		assert.Equal(t, "https://provider/v1/files/file-out/content", descriptor["url"])
	})

	t.Run("token totals are summed across every output line", func(t *testing.T) {
		outputFile := `{"custom_id":"a","response":{"body":{"usage":{"completion_tokens":10,"total_tokens":30}}}}
{"custom_id":"b","response":{"body":{"usage":{"completion_tokens":5,"total_tokens":12}}}}

{"custom_id":"c","response":{"body":{"choices":[]}}}`
		result := callBatchHook(t, plugin, "parseTaskResult",
			queryContext(map[string]any{"phase": "usage", "outputFileId": "file-out", "batch": map[string]any{"status": "completed"}}),
			outputFile, map[string]any{"status": 200})
		assert.Equal(t, "SUCCESS", result["status"])
		assert.EqualValues(t, 42, result["totalTokens"])
		assert.EqualValues(t, 15, result["completionTokens"])
	})

	t.Run("an unreadable output file still lands the batch as done", func(t *testing.T) {
		// The provider already did the work; failing here would refund it.
		result := callBatchHook(t, plugin, "parseTaskResult",
			queryContext(map[string]any{"phase": "usage", "outputFileId": "file-out", "batch": map[string]any{"status": "completed"}}),
			"", map[string]any{"status": 404})
		assert.Equal(t, "SUCCESS", result["status"])
		assert.Nil(t, result["totalTokens"])
	})

	t.Run("a batch where every request failed is refunded, not billed", func(t *testing.T) {
		// The provider still calls this "completed". Reporting success would
		// leave the estimate charged for output nobody received.
		result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil), map[string]any{
			"status": "completed", "output_file_id": "", "error_file_id": "file-err",
			"request_counts": map[string]any{"total": 3, "completed": 0, "failed": 3},
		}, map[string]any{"status": 200})
		assert.Equal(t, "FAILURE", result["status"])
		assert.Contains(t, result["reason"], "every request in the batch failed")
	})

	t.Run("a partly failed batch still settles from its output", func(t *testing.T) {
		result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil), map[string]any{
			"status": "completed", "output_file_id": "file-out",
			"request_counts": map[string]any{"total": 3, "completed": 2, "failed": 1},
		}, map[string]any{"status": 200})
		assert.Equal(t, "IN_PROGRESS", result["status"])
	})

	t.Run("terminal provider states end the task", func(t *testing.T) {
		for providerStatus, expected := range map[string]string{
			"failed": "FAILURE", "expired": "FAILURE", "cancelled": "FAILURE", "queuing": "QUEUED",
		} {
			result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil),
				map[string]any{"status": providerStatus}, map[string]any{"status": 200})
			assert.Equal(t, expected, result["status"], providerStatus)
		}
	})

	t.Run("an unrecognized state is reported as unknown", func(t *testing.T) {
		// Guessing IN_PROGRESS here would poll a dead batch until it timed out.
		result := callBatchHook(t, plugin, "parseTaskResult", queryContext(nil),
			map[string]any{"status": "something_new"}, map[string]any{"status": 200})
		assert.Equal(t, "UNKNOWN", result["status"])
	})
}

// A batch's deliverable is a file the provider holds, and /v1/files only
// resolves ids the gateway issued at upload. Artifacts are the only way a
// caller reaches it.
func TestBatchPublishesItsResultFilesAsArtifacts(t *testing.T) {
	plugin := loadBatchPlugin(t)
	batch := map[string]any{"output_file_id": "file-out", "error_file_id": "file-err"}

	listArtifacts := func(t *testing.T, task map[string]any) []map[string]any {
		t.Helper()
		value, err := plugin.Engine.Call(t.Context(), "listArtifacts", task)
		require.NoError(t, err)
		encoded, err := common.Marshal(value)
		require.NoError(t, err)
		var decoded []map[string]any
		require.NoError(t, common.Unmarshal(encoded, &decoded))
		return decoded
	}

	t.Run("a finished batch publishes both of its files", func(t *testing.T) {
		artifacts := listArtifacts(t, map[string]any{"status": "SUCCESS", "data": batch})
		require.Len(t, artifacts, 2)
		assert.Equal(t, "output", artifacts[0]["key"])
		assert.Equal(t, "file", artifacts[0]["type"])
		assert.Equal(t, "errors", artifacts[1]["key"])
	})

	t.Run("a batch that failed still publishes its error file", func(t *testing.T) {
		artifacts := listArtifacts(t, map[string]any{
			"status": "FAILURE", "data": map[string]any{"error_file_id": "file-err"},
		})
		require.Len(t, artifacts, 1)
		assert.Equal(t, "errors", artifacts[0]["key"])
	})

	t.Run("a running batch publishes nothing", func(t *testing.T) {
		assert.Empty(t, listArtifacts(t, map[string]any{"status": "IN_PROGRESS", "data": batch}))
	})

	t.Run("content is fetched from the provider with the channel credential", func(t *testing.T) {
		descriptor := callBatchHook(t, plugin, "buildContentRequest", map[string]any{
			"artifactKey": "output", "data": batch, "baseUrl": "https://provider", "apiKey": "secret",
			"clientRequest": map[string]any{"method": "GET"},
		})
		assert.Equal(t, "https://provider/v1/files/file-out/content", descriptor["url"])
		assert.Equal(t, "GET", descriptor["method"])
	})

	t.Run("an unknown artifact is refused", func(t *testing.T) {
		_, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{
			"artifactKey": "imagined", "data": batch, "baseUrl": "https://provider",
			"clientRequest": map[string]any{"method": "GET"},
		})
		require.Error(t, err)
	})
}
