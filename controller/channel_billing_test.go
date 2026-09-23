package controller

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDeepSeekBalanceCNY(t *testing.T) {
	for _, test := range []struct {
		name, responseJSON string
		rate, want         float64
		wantError          string
	}{
		{"CNY before USD keeps historical unit", `{"balance_infos":[{"currency":"CNY","total_balance":"73"},{"currency":"USD","total_balance":"12.5"}]}`, 7.3, 73, ""},
		{"CNY after USD keeps historical unit", `{"balance_infos":[{"currency":"USD","total_balance":"12.5"},{"currency":"CNY","total_balance":"73"}]}`, 7.3, 73, ""},
		{"USD only converts to CNY", `{"balance_infos":[{"currency":"USD","total_balance":"10"}]}`, 7.3, 73, ""},
		{"negative CNY preserves exhausted key", `{"balance_infos":[{"currency":"CNY","total_balance":"-7.3"}]}`, 7.3, -7.3, ""},
		{"negative USD preserves exhausted key", `{"balance_infos":[{"currency":"USD","total_balance":"-1"}]}`, 7.3, -7.3, ""},
		{"CNY does not need exchange rate", `{"balance_infos":[{"currency":"CNY","total_balance":"73"}]}`, 0, 73, ""},
		{"missing currency", `{"balance_infos":[{"currency":"EUR","total_balance":"10"}]}`, 7.3, 0, "currency USD or CNY not found"},
		{"invalid preferred currency is not hidden", `{"balance_infos":[{"currency":"CNY","total_balance":"invalid"},{"currency":"USD","total_balance":"10"}]}`, 7.3, 0, "invalid syntax"},
		{"NaN USD", `{"balance_infos":[{"currency":"USD","total_balance":"NaN"}]}`, 7.3, 0, "USD balance must be finite"},
		{"infinite CNY", `{"balance_infos":[{"currency":"CNY","total_balance":"+Inf"}]}`, 7.3, 0, "CNY balance must be finite"},
		{"zero exchange rate", `{"balance_infos":[{"currency":"USD","total_balance":"10"}]}`, 0, 0, "USD exchange rate must be greater than zero"},
		{"NaN exchange rate", `{"balance_infos":[{"currency":"USD","total_balance":"10"}]}`, math.NaN(), 0, "USD exchange rate must be finite"},
		{"infinite exchange rate", `{"balance_infos":[{"currency":"USD","total_balance":"10"}]}`, math.Inf(1), 0, "USD exchange rate must be finite"},
		{"conversion overflow", `{"balance_infos":[{"currency":"USD","total_balance":"1.7976931348623157e+308"}]}`, 2, 0, "converted CNY balance must be finite"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response DeepSeekUsageResponse
			require.NoError(t, common.UnmarshalJsonStr(test.responseJSON, &response))
			balance, err := getDeepSeekBalanceCNY(response, test.rate)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, test.want, balance, 1e-12)
		})
	}
}
