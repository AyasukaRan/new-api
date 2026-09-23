package helper

import (
	"github.com/QuantumNous/new-api/pkg/modelmapping"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// ResolveMappedModel follows a channel's model_mapping chain and reports the
// upstream name for originModel, and whether any rename applied.
//
// It is shared rather than inlined because a model reaches a provider by more
// than one route: a request body carries it, and so does every line of an
// uploaded batch file. Both have to arrive under the same upstream name.
func ResolveMappedModel(modelMapping string, originModel string) (string, bool, error) {
	return modelmapping.ResolveMappedModel(modelMapping, originModel)
}

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	upstreamModel, mapped, err := ResolveMappedModel(c.GetString("model_mapping"), info.OriginModelName)
	if err != nil {
		return err
	}
	if mapped {
		info.IsModelMapped = true
		info.UpstreamModelName = upstreamModel
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
