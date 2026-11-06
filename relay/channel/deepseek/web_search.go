package deepseek

import (
	"errors"
	"math"
	"strconv"

	"github.com/MoeForge/moego/ojson"
)

// DeepSeek reports successful queries and page reads in the final usage.
// One search item can issue several queries or return no billable results.
func responsesToolUsage(raw []byte) map[string]int {
	usage := ojson.GetBytes(raw, "usage.server_tool_use")
	if !usage.Exists() {
		usage = ojson.GetBytes(raw, "response.usage.server_tool_use")
	}
	if !usage.IsObject() {
		return nil
	}
	counts := make(map[string]int, 2)
	for _, key := range []struct{ field, price string }{
		{"tool_search", SearchToolPriceKey}, {"tool_fetch", FetchToolPriceKey},
	} {
		value := usage.Get(key.field)
		if value.Type != ojson.Number {
			continue
		}
		count, err := strconv.ParseUint(value.Raw, 10, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			continue
		}
		// Settlement applies and logs the common per-request tool limit.
		counts[key.price] = int(min(count, uint64(math.MaxInt)))
	}
	return counts
}
