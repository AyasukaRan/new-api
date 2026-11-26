package controller

import (
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type sub2APIManagementRequest struct {
	Email                 string `json:"email"`
	PasswordEncrypted     string `json:"password_encrypted"`
	CredentialEncrypted   string `json:"credential_encrypted"`
	EncryptionKeyID       string `json:"encryption_key_id"`
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
	Flow                  string `json:"flow"`
	TOTPCode              string `json:"totp_code"`
	AccountID             int64  `json:"account_id"`
	Origin                string `json:"origin"`
	ProxyEncrypted        string `json:"proxy_encrypted"`
	KeyIdentity           string `json:"key_identity"`
	SyncRates             bool   `json:"sync_rates"`
	SyncBills             bool   `json:"sync_bills"`
}

// Message keys are fixed rather than copied from untrusted upstream errors.
func sub2APIManagementError(c *gin.Context, err error) {
	code := service.Sub2APIErrorCode(err)
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": code, "message": sub2APIErrorMessage(code)})
}

func sub2APIErrorMessage(code string) string {
	if code == "" {
		return ""
	}
	messages := map[string]string{
		"SUB2API_ENCRYPTION_UNAVAILABLE": "Configure a stable Sub2API credential encryption secret first.",
		"SUB2API_STORAGE_UNAVAILABLE":    "Sub2API account storage is unavailable.",
		"SUB2API_CHANNEL_INVALID":        "Select a Sub2API channel.",
		"SUB2API_HTTPS_REQUIRED":         "Sub2API account connections require HTTPS.",
		"SUB2API_CREDENTIAL_INVALID":     "The Sub2API credential is invalid.",
		"SUB2API_LOGIN_REQUIRED":         "Sign in to the Sub2API account again.",
		"SUB2API_ACCESS_DENIED":          "The Sub2API account does not allow this action.",
		"SUB2API_UNSUPPORTED":            "This Sub2API server does not support this capability.",
		"SUB2API_RATE_LIMITED":           "Sub2API rate limited this operation. Try again later.",
		"SUB2API_CAPTCHA_REQUIRED":       "Complete verification on the Sub2API website, then import its account token.",
		"SUB2API_NO_MATCHING_KEYS":       "This Sub2API account does not own any keys in this channel.",
		"SUB2API_CHANNEL_CHANGED":        "The channel connection changed. Connect the Sub2API account again.",
		"SUB2API_FLOW_EXPIRED":           "The Sub2API verification expired or was already used. Sign in again.",
		"SUB2API_2FA_INVALID":            "Enter the six-digit Sub2API verification code.",
		"SUB2API_ACCOUNT_NOT_FOUND":      "The Sub2API account was not found.",
		"SUB2API_RESPONSE_INVALID":       "The Sub2API response is incomplete or invalid.",
		"SUB2API_KEY_NOT_BOUND":          "Connect the account that owns this key before syncing bills.",
		"SUB2API_ACCOUNT_CONFLICT":       "This Sub2API account already has an independent connection.",
		"SUB2API_ACCOUNT_LINKED":         "Detach this account from its channels before disconnecting.",
		"SUB2API_SECURE_ENTRY_REQUIRED":  "Use the HTTPS management address to connect a Sub2API account.",
	}
	message := messages[code]
	if message == "" {
		message = "Sub2API synchronization failed. Try again later."
	}
	return message
}

func GetSub2APIChannel(c *gin.Context) {
	channelID, ok := pluginLoginChannelID(c)
	if !ok {
		return
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel.Type != constant.ChannelTypeSub2API {
		common.ApiErrorT(c, "Channel does not exist")
		return
	}
	if !model.Sub2APIStorageReady() {
		common.ApiSuccess(c, gin.H{"encryption_ready": false, "management_allowed": sub2APIManagementAllowed(c.Request), "accounts": []any{}, "available_accounts": []any{}, "keys": []any{}})
		return
	}
	accounts, err := model.ListSub2APIAccounts(channelID)
	if err != nil {
		sub2APIManagementError(c, err)
		return
	}
	for i := range accounts {
		accounts[i].LastError = sub2APIErrorMessage(accounts[i].LastError)
	}
	bindings, err := model.ListSub2APIKeyBindings(channelID)
	if err != nil {
		sub2APIManagementError(c, err)
		return
	}
	byKey := make(map[string]model.Sub2APIKeyBinding, len(bindings))
	billStatus, err := model.GetSub2APIBillStatus(c.Request.Context(), channelID)
	if err != nil {
		sub2APIManagementError(c, err)
		return
	}
	for _, binding := range bindings {
		byKey[binding.KeyIdentity] = binding
	}
	rows := make([]gin.H, 0)
	for index, key := range channel.GetKeys() {
		identity := channel.KeyIdentity(key)
		binding := byKey[identity]
		bills := billStatus[identity]
		var rate *model.Sub2APIRate
		if binding.RateJSON != "" {
			_ = common.UnmarshalJsonStr(binding.RateJSON, &rate)
		}
		preview := "••••"
		if len(key) >= 8 {
			preview += key[len(key)-4:]
		}
		rows = append(rows, gin.H{"key_identity": identity, "key_index": index, "key_preview": preview, "account_id": binding.AccountID, "upstream_api_key_id": binding.UpstreamAPIKeyID, "upstream_group_id": binding.UpstreamGroupID, "sync_rates": binding.SyncRates, "sync_bills": binding.SyncBills, "rate": rate, "last_sync_at": binding.LastSyncAt, "last_error": sub2APIErrorMessage(binding.LastError), "bills_pending": bills.Pending, "bills_synced": bills.Synced, "bill_last_synced_at": bills.LastSyncedAt})
	}
	if accounts == nil {
		accounts = []model.Sub2APIAccount{}
	}
	available := make([]model.Sub2APIAccount, 0)
	origin, originErr := service.NormalizeSub2APIOrigin(channel.GetBaseURL())
	all, listErr := model.ListSub2APIAccounts(0)
	if listErr != nil {
		sub2APIManagementError(c, listErr)
		return
	}
	linked := make(map[int64]bool)
	for _, account := range accounts {
		linked[account.ID] = true
	}
	for _, account := range all {
		accountOrigin, accountOriginErr := service.NormalizeSub2APIOrigin(account.Origin)
		if originErr == nil && accountOriginErr == nil && account.SharedIdentity != nil && accountOrigin == origin && !linked[account.ID] {
			account.LastError = sub2APIErrorMessage(account.LastError)
			available = append(available, account)
		}
	}
	common.ApiSuccess(c, gin.H{"encryption_ready": service.Sub2APIEncryptionReady(), "management_allowed": sub2APIManagementAllowed(c.Request), "accounts": accounts, "available_accounts": available, "keys": rows})
}

// ManageSub2APIChannel handles the narrow authenticated account operations.
// Step-up proofs bind the action, channel, account and optional physical key.
func ManageSub2APIChannel(c *gin.Context) {
	channelID, ok := pluginLoginChannelID(c)
	if !ok {
		return
	}
	if !sub2APIManagementAllowed(c.Request) {
		sub2APIManagementError(c, &service.Sub2APIError{Code: "SUB2API_SECURE_ENTRY_REQUIRED"})
		return
	}
	identity, ok := middleware.GetStepUpIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96<<10)
	var input sub2APIManagementRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var action string
	// FullPath may have a test router prefix. The registered endpoint suffix is stable.
	if strings.HasSuffix(c.FullPath(), "/login/2fa") {
		action = "login/2fa"
	} else {
		parts := strings.Split(c.FullPath(), "/")
		action = parts[len(parts)-1]
	}
	var authorization *model.AuthFlowAuthorization
	if action != "login/2fa" {
		context, _ := common.Marshal(service.Sub2APIManageContext{ChannelID: channelID, Action: action, AccountID: input.AccountID, KeyIdentity: input.KeyIdentity})
		authorization = middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeSub2APIManage, Context: context})
		if authorization == nil {
			return
		}
	}
	if input.AccountID > 0 && action != "attach" && action != "detach" {
		account, err := model.GetSub2APIAccount(input.AccountID)
		if err != nil || account.ChannelID != channelID && account.SharedIdentity == nil {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		if account.SharedIdentity != nil {
			if action != "sync" {
				common.ApiErrorT(c, "Invalid parameters")
				return
			}
			accounts, listErr := model.ListSub2APIAccounts(channelID)
			linked := false
			for _, candidate := range accounts {
				linked = linked || candidate.ID == account.ID
			}
			if listErr != nil || !linked {
				common.ApiErrorT(c, "Invalid parameters")
				return
			}
		}
	}
	var result any = gin.H{}
	var err error
	switch action {
	case "login":
		var password string
		password, err = common.DecryptChannelCredential(input.PasswordEncrypted, input.EncryptionKeyID)
		if err == nil {
			result, err = service.LoginSub2API(c.Request.Context(), channelID, service.Sub2APILoginInput{Email: input.Email, Password: password, TurnstileToken: input.TurnstileToken, TencentCaptchaTicket: input.TencentCaptchaTicket, TencentCaptchaRandstr: input.TencentCaptchaRandstr}, authorization)
		}
	case "login/2fa":
		result, err = service.CompleteSub2APILogin(c.Request.Context(), channelID, input.Flow, input.TOTPCode, identity)
	case "import":
		var plain string
		plain, err = common.DecryptChannelCredential(input.CredentialEncrypted, input.EncryptionKeyID)
		if err == nil {
			var credential service.Sub2APICredential
			err = common.UnmarshalJsonStr(plain, &credential)
			if err == nil {
				result, err = service.ImportSub2API(c.Request.Context(), channelID, credential, authorization)
			}
		}
	case "refresh":
		err = service.RefreshSub2APIAccount(c.Request.Context(), input.AccountID, 0)
	case "disconnect":
		err = service.DisconnectSub2API(c.Request.Context(), channelID, input.AccountID, authorization)
	case "sync":
		err = service.SyncSub2APIChannel(c.Request.Context(), channelID, input.AccountID)
		_, billErr := service.SyncSub2APIBills(c.Request.Context(), channelID)
		if err == nil {
			err = billErr
		}
	case "attach":
		err = service.AttachIndependentSub2API(c.Request.Context(), channelID, input.AccountID, authorization)
	case "detach":
		err = model.DetachSub2APIAccount(channelID, input.AccountID, authorization)
	case "keys":
		if input.SyncBills {
			binding, lookupErr := model.GetSub2APIKeyBinding(channelID, input.KeyIdentity)
			if lookupErr != nil || binding.AccountID == 0 || binding.UpstreamAPIKeyID == 0 {
				err = &service.Sub2APIError{Code: "SUB2API_KEY_NOT_BOUND"}
				break
			}
		}
		err = model.SetSub2APIKeySync(channelID, input.KeyIdentity, input.SyncRates, input.SyncBills, authorization)
	default:
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err != nil {
		sub2APIManagementError(c, err)
		recordManageAudit(c, "channel.update", map[string]any{"id": channelID, "source": "sub2api", "action": action, "account_id": input.AccountID, "outcome": "failure", "error_code": service.Sub2APIErrorCode(err)})
		return
	}
	recordManageAudit(c, "channel.update", map[string]any{"id": channelID, "source": "sub2api", "action": action, "account_id": input.AccountID, "changed_fields": []string{"sub2api_account"}})
	common.ApiSuccess(c, result)
}

func sub2APIManagementAllowed(request *http.Request) bool {
	// HTTP compatibility is deployment-only; it does not make the connection secure.
	return sub2APISecureEntry(request) || common.GetEnvOrDefaultBool("SUB2API_ALLOW_HTTP_MANAGEMENT", false)
}

// Only a TLS terminator explicitly configured for this credential endpoint may
// attest HTTPS. Generic private-network or FRP peers are not trusted by default.
func sub2APISecureEntry(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	protocols := request.Header.Values("X-Forwarded-Proto")
	if len(protocols) != 1 || strings.TrimSpace(protocols[0]) != "https" {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return false
	}
	for value := range strings.SplitSeq(os.Getenv("SUB2API_TRUSTED_HTTPS_PROXIES"), ",") {
		value = strings.TrimSpace(value)
		if ip := net.ParseIP(value); ip != nil && ip.Equal(peer) {
			return true
		}
		if _, network, err := net.ParseCIDR(value); err == nil && network.Contains(peer) {
			return true
		}
	}
	return false
}

// Account management is global, but exposes only connection metadata. Neither
// proxy credentials nor access/refresh tokens are serializable in these DTOs.
func GetSub2APIAccounts(c *gin.Context) {
	if !model.Sub2APIStorageReady() {
		common.ApiSuccess(c, gin.H{"encryption_ready": false, "management_allowed": sub2APIManagementAllowed(c.Request), "accounts": []any{}})
		return
	}
	accounts, err := model.ListSub2APIAccounts(0)
	if err != nil {
		sub2APIManagementError(c, err)
		return
	}
	bindings, err := model.ListSub2APIKeyBindings(0)
	if err != nil {
		sub2APIManagementError(c, err)
		return
	}
	ids := make([]int, 0)
	for _, binding := range bindings {
		if binding.AccountID > 0 {
			ids = append(ids, binding.ChannelID)
		}
	}
	var channels []model.Channel
	if len(ids) > 0 {
		if err := model.DB.Select("id", "name").Where("id IN ? AND type = ?", ids, constant.ChannelTypeSub2API).Find(&channels).Error; err != nil {
			sub2APIManagementError(c, err)
			return
		}
	}
	names := make(map[int]string)
	for _, channel := range channels {
		names[channel.Id] = channel.Name
	}
	type channelLabel struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type accountDTO struct {
		model.Sub2APIAccount
		HasProxy   bool           `json:"has_proxy"`
		Legacy     bool           `json:"legacy"`
		ChannelIDs []int          `json:"channel_ids"`
		Channels   []channelLabel `json:"channels"`
	}
	result := make([]accountDTO, 0, len(accounts))
	for _, account := range accounts {
		account.LastError = sub2APIErrorMessage(account.LastError)
		row := accountDTO{Sub2APIAccount: account, HasProxy: service.Sub2APIAccountHasProxy(&account), Legacy: account.SharedIdentity == nil, ChannelIDs: []int{}, Channels: []channelLabel{}}
		seen := make(map[int]bool)
		for _, binding := range bindings {
			if _, exists := names[binding.ChannelID]; !exists {
				continue
			}
			if binding.AccountID == account.ID && !seen[binding.ChannelID] {
				seen[binding.ChannelID] = true
				row.ChannelIDs = append(row.ChannelIDs, binding.ChannelID)
				row.Channels = append(row.Channels, channelLabel{binding.ChannelID, names[binding.ChannelID]})
			}
		}
		result = append(result, row)
	}
	common.ApiSuccess(c, gin.H{"encryption_ready": service.Sub2APIEncryptionReady(), "management_allowed": sub2APIManagementAllowed(c.Request), "accounts": result})
}

func ManageSub2APIAccounts(c *gin.Context) {
	if !sub2APIManagementAllowed(c.Request) {
		sub2APIManagementError(c, &service.Sub2APIError{Code: "SUB2API_SECURE_ENTRY_REQUIRED"})
		return
	}
	identity, ok := middleware.GetStepUpIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96<<10)
	var input sub2APIManagementRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	action := c.FullPath()[strings.LastIndex(c.FullPath(), "/")+1:]
	if strings.HasSuffix(c.FullPath(), "/login/2fa") {
		action = "login/2fa"
	}
	operation := service.Sub2APIAccountManageContext{Action: action, AccountID: input.AccountID, Origin: input.Origin, ProxyEncrypted: input.ProxyEncrypted, Email: input.Email}
	var authorization *model.AuthFlowAuthorization
	if action != "login/2fa" {
		context, _ := common.Marshal(operation)
		authorization = middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeSub2APIAccountManage, Context: context})
		if authorization == nil {
			return
		}
	}
	var result any = gin.H{}
	var err error
	proxy := ""
	if input.ProxyEncrypted != "" {
		proxy, err = common.DecryptChannelCredential(input.ProxyEncrypted, input.EncryptionKeyID)
	}
	if err == nil {
		switch action {
		case "login":
			var password string
			password, err = common.DecryptChannelCredential(input.PasswordEncrypted, input.EncryptionKeyID)
			if err == nil {
				result, err = service.LoginIndependentSub2API(c.Request.Context(), operation, proxy, service.Sub2APILoginInput{Email: input.Email, Password: password, TurnstileToken: input.TurnstileToken, TencentCaptchaTicket: input.TencentCaptchaTicket, TencentCaptchaRandstr: input.TencentCaptchaRandstr}, authorization)
			}
		case "login/2fa":
			result, err = service.CompleteIndependentSub2APILogin(c.Request.Context(), input.Flow, input.TOTPCode, identity)
		case "import":
			var plain string
			plain, err = common.DecryptChannelCredential(input.CredentialEncrypted, input.EncryptionKeyID)
			if err == nil {
				var credential service.Sub2APICredential
				err = common.UnmarshalJsonStr(plain, &credential)
				if err == nil {
					result, err = service.ImportIndependentSub2API(c.Request.Context(), operation, proxy, credential, authorization)
				}
			}
		case "refresh":
			err = service.RefreshSub2APIAccount(c.Request.Context(), input.AccountID, 0)
		case "disconnect":
			err = service.DisconnectIndependentSub2API(c.Request.Context(), input.AccountID, authorization)
		default:
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
	}
	fields := map[string]any{"source": "sub2api_account", "action": action, "account_id": input.AccountID}
	if err != nil {
		fields["outcome"], fields["error_code"] = "failure", service.Sub2APIErrorCode(err)
		recordManageAudit(c, "channel.update", fields)
		sub2APIManagementError(c, err)
		return
	}
	fields["changed_fields"] = []string{"sub2api_account"}
	recordManageAudit(c, "channel.update", fields)
	common.ApiSuccess(c, result)
}
