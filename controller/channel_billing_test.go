package controller

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
		{"zero USD before CNY uses funded CNY", `{"balance_infos":[{"currency":"USD","total_balance":"0"},{"currency":"CNY","total_balance":"73"}]}`, 7.3, 73, ""},
		{"zero USD after CNY uses funded CNY", `{"balance_infos":[{"currency":"CNY","total_balance":"73"},{"currency":"USD","total_balance":"0"}]}`, 7.3, 73, ""},
		{"zero USD alone stays zero", `{"balance_infos":[{"currency":"USD","total_balance":"0"}]}`, 7.3, 0, ""},
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

func TestUpdateChannelNewAPIBalanceUsesTheSmallerRemainingQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   string
		want    float64
		wantErr string
	}{
		{name: "user quota below the key quota", usage: `{"code":true,"data":{"total_available":2000000,"unlimited_quota":false,"user_quota":{"total_available":500000}}}`, want: 0.5},
		{name: "key quota below the user quota", usage: `{"code":true,"data":{"total_available":500000,"unlimited_quota":false,"user_quota":{"total_available":2000000}}}`, want: 0.5},
		{name: "unlimited key uses the user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true,"user_quota":{"total_available":3000000}}}`, want: 3},
		{name: "key quota alone without user quota", usage: `{"code":true,"data":{"total_available":1500000,"unlimited_quota":false}}`, want: 1.5},
		{name: "unlimited key without user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true}}`, wantErr: "Allow querying user quota"},
		{name: "upstream rejects the key", usage: `{"success":false,"message":"invalid token"}`, wantErr: "invalid token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/usage/token/":
					assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
					_, _ = w.Write([]byte(tc.usage))
				case "/api/status":
					assert.Empty(t, r.Header.Get("Authorization"), "the key is not sent to the public status endpoint")
					_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			baseURL := server.URL + "/"
			channel := &model.Channel{Type: constant.ChannelTypeNewAPI, BaseURL: &baseURL, Key: "sk-upstream", Name: "new-api-upstream"}
			require.NoError(t, model.DB.Create(channel).Error)

			if tc.wantErr != "" {
				_, err := updateChannelNewAPIBalance(context.Background(), channel)
				require.ErrorContains(t, err, tc.wantErr)
				result, err := updateChannelBalance(channel)
				require.EqualError(t, err, "balance query failed for all keys")
				require.Len(t, result.KeyBalances, 1)
				assert.Equal(t, "balance_query_failed", result.KeyBalances[0].Error, "upstream errors do not leak into monitoring")
				return
			}
			result, err := updateChannelBalance(channel)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, result.Balance, 1e-9)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.InDelta(t, tc.want, stored.Balance, 1e-9)
		})
	}
}
