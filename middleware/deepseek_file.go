package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

// PrepareDeepSeekFileReferences authorizes file references before channel/key selection.
func PrepareDeepSeekFileReferences() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		switch c.Request.URL.Path {
		case "/v1/chat/completions", "/pg/chat/completions", "/v1/messages", "/v1/responses", "/v1/responses/compact":
		default:
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid request body")
			return
		}
		body, err := storage.Bytes()
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(body) == 0 {
			c.Next()
			return
		}
		if !ojson.ValidBytes(body) || !uniqueFileReferenceJSON(ojson.ParseBytes(body), 0) {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "request JSON must have unique object fields")
			return
		}
		paths := fileReferencePaths(body)
		// DeepSeek accepts up to 600 images and 20 documents. The provider
		// classifies their actual content; filenames cannot establish that limit.
		if len(paths) > 620 {
			abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgFileReferenceLimit, map[string]any{"Max": 620}))
			return
		}
		var selected *model.RelayFile
		authorized := make(map[string]*model.RelayFile)
		for _, path := range paths {
			reference := ojson.GetBytes(body, path)
			if reference.Type != ojson.String || reference.String() == "" {
				abortWithOpenAiMessage(c, 400, "file_id must be a non-empty string")
				return
			}
			id := reference.String()
			file := authorized[id]
			if file == nil {
				file, err = model.GetRelayFile(c.GetInt("id"), id)
				if err != nil {
					abortWithOpenAiMessage(c, http.StatusNotFound, "file not found")
					return
				}
				authorized[id] = file
			}
			if file.Provider != "deepseek" && len(paths) > 20 {
				abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgFileReferenceLimit, map[string]any{"Max": 20}))
				return
			}
			if selected != nil && (selected.ChannelId != file.ChannelId || selected.KeyIdentity != file.KeyIdentity) {
				abortWithOpenAiMessage(c, http.StatusBadRequest, "files must belong to the same upstream account")
				return
			}
			selected = file
			if file.Provider == "deepseek" {
				if file.UpstreamFileId == "" || file.KeyIdentity == "" {
					abortWithOpenAiMessage(c, http.StatusNotFound, "file not found")
					return
				}
				body, err = sjson.SetBytes(body, path, file.UpstreamFileId)
				if err != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid file reference")
					return
				}
			}
		}
		if selected != nil {
			service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: selected.ChannelId, Source: dto.PinSourceRelayFile, Rank: dto.PinRankRelayFile, RetryMode: dto.PinRetrySingleAttempt})
			c.Set("relay_file_channel_id", selected.ChannelId)
			c.Set("relay_file_key_identity", selected.KeyIdentity)
			replacement, err := common.CreateBodyStorage(body)
			if err != nil {
				abortWithOpenAiMessage(c, http.StatusInternalServerError, "failed to prepare file request")
				return
			}
			common.CleanupBodyStorage(c)
			c.Set(common.KeyBodyStorage, replacement)
			c.Request.Body = io.NopCloser(replacement)
			c.Request.ContentLength = replacement.Size()
		}
		c.Next()
	}
}

// Only protocol content blocks contain resource references; arbitrary function
// arguments and text that happen to mention file_id are not rewritten.
func fileReferencePaths(body []byte) []string {
	var paths []string
	var appendBlock func(ojson.Result, string)
	appendBlock = func(block ojson.Result, path string) {
		switch block.Get("type").String() {
		case "file", "input_file", "input_image":
			if block.Get("file_id").Exists() {
				paths = append(paths, path+".file_id")
			}
			if block.Get("file.file_id").Exists() {
				paths = append(paths, path+".file.file_id")
			}
		case "document", "image":
			if block.Get("source.type").String() == "file" && block.Get("source.file_id").Exists() {
				paths = append(paths, path+".source.file_id")
			}
		case "tool_result":
			for index, child := range block.Get("content").Array() {
				appendBlock(child, fmt.Sprintf("%s.content.%d", path, index))
			}
		case "function_call_output", "custom_tool_call_output":
			for index, child := range block.Get("output").Array() {
				appendBlock(child, fmt.Sprintf("%s.output.%d", path, index))
			}
		}
	}
	for index, block := range ojson.GetBytes(body, "system").Array() {
		appendBlock(block, fmt.Sprintf("system.%d", index))
	}
	for _, root := range []string{"messages", "input"} {
		for index, message := range ojson.GetBytes(body, root).Array() {
			path := root + "." + strconv.Itoa(index)
			appendBlock(message, path)
			for contentIndex, block := range message.Get("content").Array() {
				appendBlock(block, fmt.Sprintf("%s.content.%d", path, contentIndex))
			}
		}
	}
	return paths
}

// Ambiguous duplicate fields must not select different file IDs in the gateway
// and the provider. Values are never decoded to float64 or reserialized here.
func uniqueFileReferenceJSON(value ojson.Result, depth int) bool {
	if depth > 256 {
		return false
	}
	if !value.IsObject() && !value.IsArray() {
		return true
	}
	seen := map[string]bool{}
	valid := true
	value.ForEach(func(key, child ojson.Result) bool {
		if value.IsObject() {
			name := key.String()
			if seen[name] {
				valid = false
				return false
			}
			seen[name] = true
		}
		valid = uniqueFileReferenceJSON(child, depth+1)
		return valid
	})
	return valid
}
