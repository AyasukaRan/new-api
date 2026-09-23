package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	relayFileModelContextKey  = "relay_file_model"
	relayFileRecordContextKey = "relay_file_record"
)

// scopeToBatchChannels narrows selection to the channels that may serve a
// batch, and tells the distributor to present the batch credentials.
//
// Two filters are needed rather than one. The batch filter keeps the request
// off channels an operator has not opted in. The task-plugin identity filter
// otherwise rejects every plugin channel when no plugin is expected, and a file
// upload has no plugin of its own — so the plugin that serves the model is
// named here, which is what lets one vendor channel carry both chat and batch.
func scopeToBatchChannels(c *gin.Context, modelName string) {
	service.GetChannelConstraints(c).AddFilter(dto.ChannelFilter{Kind: dto.FilterBatchCapable})
	if modelName == "" || c.GetString("expected_task_plugin_key") != "" {
		return
	}
	generation := pluginruntime.DefaultRegistry.Generation()
	plugin, found := generation.GetByModel(modelName)
	if !found || plugin == nil {
		return
	}
	c.Set("expected_task_plugin_key", plugin.Meta.Key)
	// Naming the plugin is not enough: the identity filter admits a vendor
	// channel only through the channel types of the plugin pinned in context,
	// so naming one without pinning it rejects every channel the plugin drives.
	// A file upload has no plugin route to pin it, so it is pinned here.
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{
		Generation: generation,
		Plugin:     plugin,
	})
}

// PrepareRelayFileUpload resolves the model named inside an upload before
// channel selection, so the file lands on the provider that will later be asked
// to run it.
func PrepareRelayFileUpload() gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName, err := service.BatchUploadModel(c)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error())
			return
		}
		// The distributor reads this before it inspects the path, so the upload
		// is not parsed a second time.
		c.Set("resolved_task_model", modelName)
		scopeToBatchChannels(c, modelName)
		c.Next()
	}
}

// PrepareRelayFile resolves a file id the caller claims to own and pins the
// request to the channel that stores it.
//
// The pin is not an optimization. Upstream file ids are unique only inside the
// provider account behind a channel key, so sending an id to a channel that did
// not issue it either 404s or, worse, addresses an unrelated file that happens
// to share the id. Resolution is also the ownership check: a caller who does
// not own the id never reaches an upstream request at all.
func PrepareRelayFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		fileId := c.Param("id")
		if fileId == "" {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "file id is required")
			return
		}
		file, err := model.GetRelayFile(c.GetInt("id"), fileId)
		if err != nil {
			// A caller who owns nothing is told the same thing as a caller
			// asking for an id that never existed, so the response cannot be
			// used to enumerate the shared provider account.
			if errors.Is(err, model.ErrRelayFileNotFound) {
				abortWithOpenAiMessage(c, http.StatusNotFound, "no such file: "+fileId)
				return
			}
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "failed to resolve file: "+err.Error())
			return
		}
		c.Set(relayFileRecordContextKey, file)
		c.Set(relayFileModelContextKey, file.Model)
		scopeToBatchChannels(c, file.Model)
		service.GetChannelConstraints(c).AddPin(dto.ChannelPin{
			ChannelId: file.ChannelId,
			Source:    dto.PinSourceRelayFile,
			Rank:      dto.PinRankRelayFile,
			// The bytes live on exactly one channel, so a retry elsewhere can
			// only produce a wrong answer.
			RetryMode: dto.PinRetrySingleAttempt,
		})
		c.Next()
	}
}

// PrepareRelayFileReference pins a request that consumes a previously uploaded
// file to the channel holding it.
//
// A batch names its input by id. That id only means anything on the channel
// that issued it, so without this the batch would be routed by model alone and
// could be handed to a sibling channel where the id is unknown — or, worse,
// where it names an unrelated file. Resolving the id also re-checks ownership
// and pins the model to the one the uploaded file actually declares, so the
// batch cannot be billed at a cheaper model's rate than it runs at.
func PrepareRelayFileReference() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Method != http.MethodPost ||
			!strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.Next()
			return
		}
		body, err := storage.Bytes()
		if err != nil {
			c.Next()
			return
		}
		fileId := strings.TrimSpace(gjson.GetBytes(body, "input_file_id").String())
		if fileId == "" {
			c.Next()
			return
		}
		file, err := model.GetRelayFile(c.GetInt("id"), fileId)
		if err != nil {
			if errors.Is(err, model.ErrRelayFileNotFound) {
				abortWithOpenAiMessage(c, http.StatusNotFound, "no such file: "+fileId)
				return
			}
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "failed to resolve file: "+err.Error())
			return
		}
		declared := strings.TrimSpace(gjson.GetBytes(body, "model").String())
		if declared == "" {
			declared = strings.TrimSpace(gjson.GetBytes(body, "metadata.model").String())
		}
		if declared != "" && file.Model != "" && declared != file.Model {
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf(
				"model %q does not match the uploaded file, which declares %q", declared, file.Model))
			return
		}
		c.Set(relayFileRecordContextKey, file)
		if file.Model != "" {
			c.Set("resolved_task_model", file.Model)
		}
		// Resolving an uploaded file is what makes this a batch request, so the
		// batch scope is applied here and not to every plugin route.
		scopeToBatchChannels(c, file.Model)
		service.GetChannelConstraints(c).AddPin(dto.ChannelPin{
			ChannelId: file.ChannelId,
			Source:    dto.PinSourceRelayFile,
			Rank:      dto.PinRankRelayFile,
			RetryMode: dto.PinRetrySingleAttempt,
		})
		c.Next()
	}
}

// RelayFileRecord returns the file resolved by PrepareRelayFile.
func RelayFileRecord(c *gin.Context) *model.RelayFile {
	if value, exists := c.Get(relayFileRecordContextKey); exists {
		if file, ok := value.(*model.RelayFile); ok {
			return file
		}
	}
	return nil
}
