package model

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

func applyExplicitLogTextFilter(tx *gorm.DB, column string, value string) (*gorm.DB, error) {
	if value == "" {
		return tx, nil
	}
	if strings.Contains(value, "%") {
		condition, pattern, err := buildLogLikeCondition(column, value)
		if err != nil {
			return nil, err
		}
		return tx.Where(condition, pattern), nil
	}
	return tx.Where(column+" = ?", value), nil
}

func buildLogLikeCondition(column string, value string) (string, string, error) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		pattern, err := sanitizeClickHouseLikePattern(value)
		if err != nil {
			return "", "", err
		}
		return column + " LIKE ?", pattern, nil
	}

	pattern, err := sanitizeLikePattern(value)
	if err != nil {
		return "", "", err
	}
	return column + " LIKE ? ESCAPE '!'", pattern, nil
}

func sanitizeClickHouseLikePattern(input string) (string, error) {
	input = strings.ReplaceAll(input, `\`, `\\`)
	input = strings.ReplaceAll(input, `_`, `\_`)

	if err := validateLikePattern(input); err != nil {
		return "", err
	}
	return input, nil
}

type Log struct {
	RetryCount          int    `json:"retry_count,omitempty" gorm:"-"`
	RetryGroupRequestID string `json:"retry_group_request_id,omitempty" gorm:"-"`
	RetryStartedAt      int64  `json:"retry_started_at,omitempty" gorm:"-"`
	LiveKey             string `json:"live_key,omitempty" gorm:"-"`
	Id                  int    `json:"id" gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId              int    `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt           int64  `json:"created_at" gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type                int    `json:"type" gorm:"index:idx_created_at_type"`
	Content             string `json:"content"`
	Username            string `json:"username" gorm:"index;index:index_username_model_name,priority:2;default:''"`
	UserAdminNickname   string `json:"user_admin_nickname,omitempty" gorm:"-"`
	TokenName           string `json:"token_name" gorm:"index;default:''"`
	ModelName           string `json:"model_name" gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota               int    `json:"quota" gorm:"default:0"`
	PromptTokens        int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens    int    `json:"completion_tokens" gorm:"default:0"`
	UseTime             int    `json:"use_time" gorm:"default:0"`
	IsStream            bool   `json:"is_stream"`
	ChannelId           int    `json:"channel" gorm:"index"`
	ChannelName         string `json:"channel_name" gorm:"->"`
	TokenId             int    `json:"token_id" gorm:"default:0;index"`
	Group               string `json:"group" gorm:"index"`
	Ip                  string `json:"ip" gorm:"index;default:''"`
	RequestId           string `json:"request_id,omitempty" gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId   string `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other               string `json:"other"`
}

// don't use iota, avoid change log type value
const (
	LogTypeUnknown     = 0
	LogTypeTopup       = 1
	LogTypeConsume     = 2
	LogTypeManage      = 3
	LogTypeSystem      = 4
	LogTypeError       = 5
	LogTypeRefund      = 6
	LogTypeLogin       = 7
	LogTypeTestConsume = 8
)

type LogSource string

const (
	LogSourceUsage LogSource = "usage"
	LogSourceTest  LogSource = "test"
)

// Tests now have a server-assigned type. Older probes had no type of their own;
// recognize their complete signature, never a user-controlled token name alone.
func applyLogSourceFilter(tx *gorm.DB, sources ...LogSource) *gorm.DB {
	const condition = "COALESCE(logs.type, 0) = ? OR (COALESCE(logs.type, 0) = ? AND COALESCE(logs.token_id, 0) = ? AND COALESCE(logs.token_name, '') = ? AND COALESCE(logs.content, '') = ?)"
	if len(sources) > 0 && sources[0] == LogSourceTest {
		return tx.Where("("+condition+")", LogTypeTestConsume, LogTypeConsume, 0, "模型测试", "模型测试")
	}
	return tx.Where("NOT ("+condition+")", LogTypeTestConsume, LogTypeConsume, 0, "模型测试", "模型测试")
}

// maxLoggedRequestBodyBytes caps a single captured request body. Bodies are
// stored inline in logs.other, which GetAllLogs returns for every row of a
// page, so an uncapped payload would make the admin log list unloadable.
//
// ponytail: oversized bodies are head-truncated rather than paged; add a
// single-log detail endpoint if admins need the tail of long conversations.
const maxLoggedRequestBodyBytes = 64 * 1024

// attachRequestBody records the client's request payload under
// other.admin_info.request_body when the operator has enabled capture.
// admin_info is stripped from every non-admin projection by formatUserLogs, so
// nesting is what makes the content admin-only.
//
// It only reads the body storage the relay already populated; it never calls
// GetRequestBody, which would try to drain an http.Request body that is long
// since consumed by the time a log is written.
func attachRequestBody(c *gin.Context, other *LogOther) {
	if !common.LogRequestBodyEnabled || other == nil || c == nil || c.Request == nil {
		return
	}
	// Non-JSON bodies are uploads (audio, images, multipart forms); capturing
	// them would put binary in the log and blow the cap for no audit value.
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		return
	}
	cached, exists := c.Get(common.KeyBodyStorage)
	if !exists || cached == nil {
		return
	}
	storage, ok := cached.(common.BodyStorage)
	if !ok {
		return
	}
	size := storage.Size()
	if size <= 0 {
		return
	}
	// An independent reader keeps the shared seek cursor untouched, and reading
	// at most the cap avoids materializing a disk-backed body into the heap.
	reader, err := storage.NewReader()
	if err != nil {
		return
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxLoggedRequestBodyBytes))
	if err != nil || len(body) == 0 {
		return
	}
	if size > int64(len(body)) {
		other.SetAdmin("request_body_truncated", size)
	}
	other.SetAdmin("request_body", string(body))
}

func ensureLogRequestId(log *Log) {
	if log != nil && log.RequestId == "" {
		log.RequestId = common.NewRequestId()
	}
}

func createLog(log *Log) error {
	ensureLogRequestId(log)
	// ClickHouse has no generated row ID. Distinguish separate attempts even
	// when a request produces multiple identical entries in the same second.
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) && log.Id == 0 {
		var b [8]byte
		for log.Id == 0 {
			rand.Read(b[:])
			log.Id = int(binary.BigEndian.Uint64(b[:])) & math.MaxInt
		}
	}
	if err := LOG_DB.Create(log).Error; err != nil {
		return err
	}
	if err := QueueSub2APIBill(context.Background(), log); err != nil {
		common.SysError("Sub2API bill queue failed; source log retained for reconciliation")
	}
	usageLiveFeed.committed(log)
	return nil
}

func clickHouseLogOrder(prefix string) string {
	return prefix + "created_at desc, " + prefix + "request_id desc"
}

func assignDisplayLogIds(logs []*Log, startIdx int) {
	for i := range logs {
		logs[i].Id = startIdx + i + 1
	}
}

func formatUserLogs(logs []*Log, startIdx int) {
	for i := range logs {
		appendLogErrorSummary(logs[i])
		logs[i].ChannelName = ""
		logs[i].UpstreamRequestId = ""
		logs[i].Other = formatLogOtherJSON(logs[i].Other, logOtherVisibilityUser)
	}
	assignDisplayLogIds(logs, startIdx)
}

// FormatAdminLogs removes root-only diagnostics while retaining operational
// admin_info. Root callers must not pass their results through this formatter.
func FormatAdminLogs(logs []*Log) {
	FormatLogList(logs, common.RoleAdminUser, false)
}

// FormatRootLogs normalizes legacy metadata into the current scoped shape
// without removing root-only diagnostics.
func FormatRootLogs(logs []*Log) {
	FormatLogList(logs, common.RoleRootUser, false)
}

// FormatLogList applies the administrator's existing visibility before sending
// a full list or a summary whose captured request bodies can be fetched later.
func FormatLogList(logs []*Log, role int, deferBody bool) {
	visibility := logOtherVisibilityUser
	if role >= common.RoleRootUser {
		visibility = logOtherVisibilityRoot
	} else if role >= common.RoleAdminUser {
		visibility = logOtherVisibilityAdmin
	}
	for _, log := range logs {
		appendLogErrorSummary(log)
		if visibility == logOtherVisibilityUser {
			log.UpstreamRequestId = ""
		}
		log.Other = projectLogOtherJSON(log.Other, visibility, deferBody && log.CreatedAt > 0)
	}
}

// GetLogRequestBody reads only the captured body, never the other privileged
// metadata from the same row.
func GetLogRequestBody(ctx context.Context, createdAt int64, requestID, ref string) (string, error) {
	capture, err := GetLogRequestBodyCapture(ctx, createdAt, requestID, ref)
	return capture.Body, err
}

type LogRequestBodyCapture struct {
	Body        string
	Truncated   bool
	RequestPath string
}

// GetLogRequestBodyCapture keeps completeness and the recorded ingress path
// alongside the matched capture without loading a different row on a retry.
func GetLogRequestBodyCapture(ctx context.Context, createdAt int64, requestID, ref string) (LogRequestBodyCapture, error) {
	query := LOG_DB.WithContext(ctx).Model(&Log{}).Select("other").Where("created_at = ?", createdAt)
	if requestID == "" {
		query = query.Where("request_id = '' OR request_id IS NULL")
	} else {
		query = query.Where("request_id = ?", requestID)
	}
	rows, err := query.Rows()
	if err != nil {
		return LogRequestBodyCapture{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var other *string
		if err := rows.Scan(&other); err != nil {
			return LogRequestBodyCapture{}, err
		}
		if other == nil {
			continue
		}
		var capture struct {
			RequestPath common.RawMessage `json:"request_path"`
			AdminInfo   struct {
				RequestBody string            `json:"request_body"`
				Truncated   common.RawMessage `json:"request_body_truncated"`
			} `json:"admin_info"`
		}
		if common.UnmarshalJsonStr(*other, &capture) == nil {
			body := capture.AdminInfo.RequestBody
			if body != "" && logRequestBodyRef(body) == ref {
				var path string
				_ = common.Unmarshal(capture.RequestPath, &path)
				marker := strings.TrimSpace(string(capture.AdminInfo.Truncated))
				truncated := marker != "" && marker != "0" && marker != "false" && marker != "null"
				return LogRequestBodyCapture{Body: body, Truncated: truncated, RequestPath: path}, nil
			}
		}
	}
	return LogRequestBodyCapture{}, rows.Err()
}

// Records predating the dedicated audit table can still occur in the usage
// table. Filter at query time so pagination, counts and exports cannot reveal them.
func withoutLegacyAuditLogs(tx *gorm.DB) *gorm.DB {
	return tx.Where("COALESCE(logs.type, 0) NOT IN ?", []int{LogTypeManage, LogTypeLogin})
}

func GetLogByTokenId(tokenId int) (logs []*Log, err error) {
	order := "id desc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("")
	}
	tx := withoutLegacyAuditLogs(applyLogSourceFilter(LOG_DB.Model(&Log{})))
	err = tx.Where("token_id = ?", tokenId).Order(order).Limit(common.MaxRecentItems).Find(&logs).Error
	if err != nil {
		return nil, err
	}
	formatUserLogs(logs, 0)
	return logs, resolveLogUsers(logs, false)
}

func RecordLog(userId int, logType int, content *common.Message) {
	if logType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := GetUsernameById(userId, false)
	other := NewLogOther()
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   other.setContent([]*common.Message{content}),
		Other:     other.JSONString(),
	}
	err := createLog(log)
	if err != nil {
		common.SysLog(common.LogText("failed to record log: %s", err.Error()))
	}
}

// RecordLogWithAdminInfo stores operator metadata under other.admin_info and
// an optional, user-visible operation descriptor under other.op for localization.
func RecordLogWithAdminInfo(userId int, logType int, content string, adminInfo *AuditAdminInfo, operation *AuditOperation, request ...*gin.Context) {
	if logType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := GetUsernameById(userId, false)
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   content,
	}
	if logType == LogTypeManage {
		var c *gin.Context
		if len(request) > 0 {
			c = request[0]
		}
		actorRole := 0
		if c != nil {
			actorRole = c.GetInt("role")
		}
		RecordAuditLog(c, AuditLog{UserId: userId, Username: username, ActorRole: actorRole, Category: AuditCategoryOperation, Content: content, Other: AuditOther{AdminInfo: adminInfo, Op: operation}, Success: true})
		return
	}
	if len(request) > 0 && request[0] != nil {
		log.RequestId = request[0].GetString(common.RequestIdKey)
	}
	if adminInfo != nil || operation != nil {
		data, err := common.Marshal(AuditOther{AdminInfo: adminInfo, Op: operation})
		if err != nil {
			common.SysError(common.LogText("failed to encode log admin info: %s", err.Error()))
			return
		}
		log.Other = string(data)
	}
	if err := createLog(log); err != nil {
		common.SysLog(common.LogText("failed to record log: %s", err.Error()))
	}
}

// RecordLoginLog writes new login events to the independent audit table.
// username 由调用方传入（登录流程已持有用户对象），避免额外的数据库查询。
// content 为英文兜底文本（用于导出）；action+params 供前端本地化渲染。
// other 包含 login_method、user_agent 等结构化信息。
func RecordLoginLog(userId, actorRole int, username string, content string, ip string, action string, params map[string]any, other AuditOther, request ...*gin.Context) {
	other.Op = &AuditOperation{Action: action, Params: params}
	var c *gin.Context
	if len(request) > 0 {
		c = request[0]
	}
	RecordAuditLog(c, AuditLog{UserId: userId, Username: username, ActorRole: actorRole, Category: AuditCategoryLogin, Action: action, Content: content, Ip: ip, Other: other, Success: true})
}

// RecordOperationAuditLog writes new operation/security events to the audit table.
// logUserId 为日志归属者，管理审计日志应归属实际操作者；目标资源/用户放入
// action params。username 内部按 logUserId 查询。content 为英文兜底文本（供导出使用）。
// action+params 写入 Other.op，供前端本地化渲染（普通用户可见，不含敏感信息）。
// adminInfo 存放操作者身份（写入 Other.admin_info，普通用户查询时剥离）；
// auditInfo 存放路由/方法/结果等中间件兜底信息（写入 Other.audit_info，普通用户查询时剥离）。
func RecordOperationAuditLog(logUserId, actorRole int, content string, ip string, action string, params map[string]any, adminInfo *AuditAdminInfo, auditInfo *AuditRequestInfo, request ...*gin.Context) {
	username, _ := GetUsernameById(logUserId, false)
	other := AuditOther{
		Op:        &AuditOperation{Action: action, Params: params},
		AdminInfo: adminInfo,
		AuditInfo: auditInfo,
	}
	var c *gin.Context
	if len(request) > 0 {
		c = request[0]
	}
	category := AuditCategoryOperation
	if adminInfo == nil {
		category = AuditCategorySecurity
	}
	status, success := 200, true
	if auditInfo != nil {
		status = auditInfo.Status
		success = auditInfo.Success
	}
	RecordAuditLog(c, AuditLog{UserId: logUserId, Username: username, ActorRole: actorRole, Category: category, Action: action, Content: content, Ip: ip, Status: status, Success: success, Other: other})
}

func RecordTopupLog(userId int, content *common.Message, callerIp string, paymentMethod string, callbackPaymentMethod string) {
	username, _ := GetUsernameById(userId, false)
	other := NewLogOther()
	other.MergeAdmin(map[string]any{
		"server_ip":               common.GetIp(),
		"node_name":               common.NodeName,
		"caller_ip":               callerIp,
		"payment_method":          paymentMethod,
		"callback_payment_method": callbackPaymentMethod,
		"version":                 common.Version,
	})
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeTopup,
		Content:   other.setContent([]*common.Message{content}),
		Ip:        callerIp,
		Other:     other.JSONString(),
	}
	err := createLog(log)
	if err != nil {
		common.SysLog(common.LogText("failed to record topup log: %s", err.Error()))
	}
}

func RecordErrorLog(c *gin.Context, userId int, channelId int, modelName string, tokenName string, content string, tokenId int, useTimeSeconds int,
	isStream bool, group string, other *LogOther) {
	logger.LogInfo(c, common.LogText("record error log: userId=%d, channelId=%d, modelName=%s, tokenName=%s, content=%s", userId, channelId, modelName, tokenName, common.LocalLogPreview(content)))
	username := c.GetString("username")
	requestId := c.GetString(common.RequestIdKey)
	upstreamRequestId := c.GetString(common.UpstreamRequestIdKey)
	if other == nil && common.LogRequestBodyEnabled {
		other = NewLogOther()
	}
	attachRequestBody(c, other)
	otherStr := other.JSONString()
	log := &Log{
		UserId:            userId,
		Username:          username,
		CreatedAt:         common.GetTimestamp(),
		Type:              LogTypeError,
		Content:           content,
		PromptTokens:      0,
		CompletionTokens:  0,
		TokenName:         tokenName,
		ModelName:         modelName,
		Quota:             0,
		ChannelId:         channelId,
		TokenId:           tokenId,
		UseTime:           useTimeSeconds,
		IsStream:          isStream,
		Group:             group,
		Ip:                common.RequestClientIP(c),
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherStr,
	}
	err := createLog(log)
	if err != nil {
		logger.LogError(c, common.LogText("failed to record log: %s", err.Error()))
	}
}

type RecordConsumeLogParams struct {
	IsChannelTest    bool              `json:"is_channel_test,omitempty"`
	ChannelId        int               `json:"channel_id"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	ModelName        string            `json:"model_name"`
	TokenName        string            `json:"token_name"`
	Quota            int               `json:"quota"`
	Content          []*common.Message `json:"content"`
	TokenId          int               `json:"token_id"`
	UseTimeSeconds   int               `json:"use_time_seconds"`
	IsStream         bool              `json:"is_stream"`
	Group            string            `json:"group"`
	Other            *LogOther         `json:"other"`
}

func RecordConsumeLog(c *gin.Context, userId int, params RecordConsumeLogParams) {
	// Request plugin report hooks read what the request settled, whether or
	// not consume logs are kept.
	common.SetContextKey(c, constant.ContextKeyConsumeLog, &params)
	if !common.LogConsumeEnabled {
		return
	}
	logger.LogInfo(c, common.LogText("record consume log: userId=%d, params=%s", userId, common.GetJsonString(params)))
	username := c.GetString("username")
	requestId := c.GetString(common.RequestIdKey)
	upstreamRequestId := c.GetString(common.UpstreamRequestIdKey)
	createdAt := common.GetTimestamp()
	logType := LogTypeConsume
	if params.IsChannelTest {
		logType = LogTypeTestConsume
	}
	other := params.Other
	if other == nil && (common.LogRequestBodyEnabled || len(params.Content) > 0) {
		other = NewLogOther()
	}
	if other != nil && (other.adminInfo["cost"] != nil || other.public["free_quota"] != nil) {
		other.SetRoot("node_name", common.NodeName)
	}
	attachRequestBody(c, other)
	content := other.setContent(params.Content)
	otherStr := other.JSONString()
	log := &Log{
		UserId:            userId,
		Username:          username,
		CreatedAt:         createdAt,
		Type:              logType,
		Content:           content,
		PromptTokens:      params.PromptTokens,
		CompletionTokens:  params.CompletionTokens,
		TokenName:         params.TokenName,
		ModelName:         params.ModelName,
		Quota:             params.Quota,
		ChannelId:         params.ChannelId,
		TokenId:           params.TokenId,
		UseTime:           params.UseTimeSeconds,
		IsStream:          params.IsStream,
		Group:             params.Group,
		Ip:                common.RequestClientIP(c),
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherStr,
	}
	err := createLog(log)
	if err != nil {
		logger.LogError(c, common.LogText("failed to record log: %s", err.Error()))
	}
	if common.DataExportEnabled && !params.IsChannelTest {
		var cacheReadTokens, cacheCreationTokens int
		if other != nil {
			cacheReadTokens, _ = other.public["cache_tokens"].(int)
			// The settlement path already normalizes the cache creation total.
			// Do not add the 5m/1h breakdown again or infer tokens from prices.
			cacheCreationTokens, _ = other.public["cache_write_tokens"].(int)
		}
		LogQuotaData(QuotaDataLogParams{
			UserID:              userId,
			Username:            username,
			ModelName:           params.ModelName,
			Quota:               params.Quota,
			CreatedAt:           createdAt,
			TokenUsed:           params.PromptTokens + params.CompletionTokens,
			ClientTool:          params.Other.ClientTool(),
			CacheReadTokens:     int64(cacheReadTokens),
			CacheCreationTokens: int64(cacheCreationTokens),
			UseGroup:            params.Group,
			TokenID:             params.TokenId,
			ChannelID:           params.ChannelId,
			NodeName:            common.NodeName,
		})
	}
}

type RecordTaskBillingLogParams struct {
	ClientIP  string
	UserId    int
	LogType   int
	Content   []*common.Message
	ChannelId int
	ModelName string
	Quota     int
	TokenId   int
	Group     string
	Other     *LogOther
	NodeName  string // 任务发起节点；为空时回退当前节点
}

func RecordTaskBillingLog(params RecordTaskBillingLogParams) {
	if params.LogType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := GetUsernameById(params.UserId, false)
	tokenName := ""
	if params.TokenId > 0 {
		if token, err := GetTokenById(params.TokenId); err == nil {
			tokenName = token.Name
		}
	}
	createdAt := common.GetTimestamp()
	other := params.Other
	log := &Log{
		UserId:    params.UserId,
		Ip:        params.ClientIP,
		Username:  username,
		CreatedAt: createdAt,
		Type:      params.LogType,
		Content:   other.setContent(params.Content),
		TokenName: tokenName,
		ModelName: params.ModelName,
		Quota:     params.Quota,
		ChannelId: params.ChannelId,
		TokenId:   params.TokenId,
		Group:     params.Group,
		Other:     other.JSONString(),
	}
	err := createLog(log)
	if err != nil {
		common.SysLog(common.LogText("failed to record task billing log: %s", err.Error()))
	}
	if params.LogType == LogTypeConsume && common.DataExportEnabled {
		nodeName := params.NodeName
		if nodeName == "" {
			nodeName = common.NodeName
		}
		LogQuotaData(QuotaDataLogParams{
			UserID:     params.UserId,
			Username:   username,
			ModelName:  params.ModelName,
			Quota:      params.Quota,
			CreatedAt:  createdAt,
			UseGroup:   params.Group,
			TokenID:    params.TokenId,
			ChannelID:  params.ChannelId,
			NodeName:   nodeName,
			ClientTool: params.Other.ClientTool(),
		})
	}
}

// LogFilter is the usage-log search shared by the log list and the log export.
// Username, Channel, UpstreamRequestId, and ClientTool are administrator filters.
type LogFilter struct {
	retries       *logRetryProjection
	Live          bool // Opt into stable list keys or the corresponding exact statistics count.
	AdminSelfView bool // set by the dashboard from the authenticated account; never bound from the query

	Source            LogSource
	Category          string // "model" selects relay requests without changing legacy account-history APIs.
	Result            string // Empty, "success", or "failure"; shared by snapshots, exports, stats, and SSE.
	Type              int
	StartTimestamp    int64
	EndTimestamp      int64
	ModelName         string
	ReasoningEffort   string
	ResponseTransport string
	RequestPath       string
	ClientTool        string
	IP                string
	Username          string
	TokenName         string
	Channel           int
	Group             string
	RequestId         string
	UpstreamRequestId string
}

// A registered username identifies its current owner, even if another account
// once had the same name. Unassigned names still find historical log snapshots.
func filterLogsByUsername(tx *gorm.DB, username string) (*gorm.DB, error) {
	if username == "" {
		return tx, nil
	}
	users := DB.WithContext(tx.Statement.Context).Model(&User{})
	if !strings.Contains(username, "%") {
		var id int
		if err := users.Where("username = ?", username).Limit(1).Pluck("id", &id).Error; err != nil {
			return nil, err
		}
		if id != 0 {
			return tx.Where("logs.user_id = ?", id), nil
		}
		return tx.Where("logs.username = ?", username), nil
	}
	// Wildcards can match both current and historical names. The two databases
	// need different escaping when logs are in ClickHouse.
	pattern, err := sanitizeLikePattern(username)
	if err != nil {
		return nil, err
	}
	condition, logPattern, err := buildLogLikeCondition("logs.username", username)
	if err != nil {
		return nil, err
	}
	matching := users.Where("username LIKE ? ESCAPE '!'", pattern)
	if DB == LOG_DB {
		return tx.Where("("+condition+" OR logs.user_id IN (?))", logPattern, matching.Select("id")), nil
	}
	var ids []int
	if err := matching.Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return tx.Where(condition, logPattern), nil
	}
	// The log database cannot join users across connections. A numeric list
	// of IDs from the main database avoids per-dialect bind parameter limits.
	var list strings.Builder
	for i, id := range ids {
		if i > 0 {
			list.WriteByte(',')
		}
		list.WriteString(strconv.Itoa(id))
	}
	return tx.Where("("+condition+" OR logs.user_id IN ("+list.String()+"))", logPattern), nil
}

func (f LogFilter) apply(tx *gorm.DB) (*gorm.DB, error) {
	var err error
	if err = f.Validate(); err != nil {
		return nil, err
	}
	tx = f.retries.apply(applyLogSourceFilter(tx, f.Source))
	if f.Category == "model" || f.Result != "" {
		tx = tx.Where("logs.type IN ?", []int{LogTypeConsume, LogTypeError})
	}
	if f.Result != "" {
		tx = tx.Where(logResultFilterSQL()+" = ?", f.Result)
	}
	if f.Type != LogTypeUnknown {
		if f.Source == LogSourceTest && (f.Type == LogTypeConsume || f.Type == LogTypeTestConsume) {
			tx = tx.Where("logs.type IN ?", []int{LogTypeConsume, LogTypeTestConsume})
		} else {
			tx = tx.Where("logs.type = ?", f.Type)
		}
	}
	if tx, err = applyExplicitLogTextFilter(tx, "logs.model_name", f.ModelName); err != nil {
		return nil, err
	}
	if tx, err = filterLogsByUsername(tx, f.Username); err != nil {
		return nil, err
	}
	if f.IP != "" {
		tx = tx.Where("logs.ip = ?", f.IP)
	}
	if f.ReasoningEffort != "" {
		tx = filterLogMetadataText(tx, "reasoning_effort", f.ReasoningEffort)
	}
	if f.ResponseTransport != "" {
		switch f.ResponseTransport {
		case "sse", "ws", "http", "sync", "async", "unknown", "non_streamed":
			tx = tx.Where("logs.type IN ?", []int{LogTypeConsume, LogTypeError, LogTypeTestConsume}).
				Where(logTransportFilterSQL()+" = ?", f.ResponseTransport)
		default:
			return nil, common.NewMessage("Invalid parameters")
		}
	}
	if f.RequestPath != "" {
		tx = tx.Where("logs.type IN ?", []int{LogTypeUnknown, LogTypeConsume, LogTypeError, LogTypeTestConsume})
		path := logRequestPathSQL()
		if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			tx = tx.Where("left("+path+", 128) = left(CAST(? AS text), 128)", f.RequestPath)
		}
		if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) || len(f.RequestPath) >= 128 {
			tx = tx.Where(path+" = ?", f.RequestPath)
		}
	}
	if f.ClientTool != "" {
		tx = filterLogMetadataText(tx, "client_tool", f.ClientTool)
	}
	if f.TokenName != "" {
		tx = tx.Where("logs.token_name = ?", f.TokenName)
	}
	if f.RequestId != "" {
		tx = tx.Where("logs.request_id = ?", f.RequestId)
	}
	if f.UpstreamRequestId != "" {
		tx = tx.Where("logs.upstream_request_id = ?", f.UpstreamRequestId)
	}
	if f.StartTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", f.StartTimestamp)
	}
	if f.EndTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", f.EndTimestamp)
	}
	if f.Channel != 0 {
		tx = tx.Where("logs.channel_id = ?", f.Channel)
	}
	if f.Group != "" {
		tx = tx.Where("logs."+logGroupCol+" = ?", f.Group)
	}
	return tx, nil
}

type logUserIdentity struct {
	Id       int
	Username string
	Remark   string
}

// The main database may be separate from the log database. Only administrator
// log views select the private note, and only its first nonempty line is used.
func currentLogUsers(ctx context.Context, ids []int, includeAdminNickname bool) (map[int]logUserIdentity, error) {
	users := make(map[int]logUserIdentity, len(ids))
	columns := []string{"id", "username"}
	if includeAdminNickname {
		columns = append(columns, "remark")
	}
	for batch := range slices.Chunk(ids, 500) {
		var records []logUserIdentity
		if err := DB.WithContext(ctx).Model(&User{}).Select(columns).Where("id IN ?", batch).Find(&records).Error; err != nil {
			return nil, err
		}
		for _, user := range records {
			first, _, _ := strings.Cut(strings.TrimSpace(user.Remark), "\n")
			user.Remark = strings.TrimSpace(first)
			users[user.Id] = user
		}
	}
	return users, nil
}

func (l *Log) useCurrentUser(users map[int]logUserIdentity) {
	user, ok := users[l.UserId]
	l.UserAdminNickname = user.Remark
	if ok {
		l.Username = user.Username
	}
}

func resolveLogUsers(logs []*Log, includeAdminNickname bool) error {
	ids := types.NewSet[int]()
	for _, log := range logs {
		if log.UserId > 0 {
			ids.Add(log.UserId)
		}
	}
	users, err := currentLogUsers(context.Background(), ids.Items(), includeAdminNickname)
	if err != nil {
		return err
	}
	for _, log := range logs {
		log.useCurrentUser(users)
	}
	return nil
}

func GetAllLogs(filter LogFilter, startIdx int, num int) (logs []*Log, total int64, err error) {
	filter, err = filter.withRetries(context.Background(), 0)
	if err != nil {
		return nil, 0, err
	}
	tx, err := filter.apply(LOG_DB)
	if err != nil {
		return nil, 0, err
	}
	err = tx.Model(&Log{}).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}
	order := "logs.created_at desc, logs.id desc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("logs.")
	}
	err = tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}
	filter.retries.annotate(logs)
	if filter.Live {
		keyLiveLogs(logs)
	}
	if err := resolveLogChannels(logs); err != nil {
		return nil, 0, err
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assignDisplayLogIds(logs, startIdx)
	}
	return logs, total, resolveLogUsers(logs, true)
}

func resolveLogChannels(logs []*Log) error {
	if err := AttachSub2APIBills(context.Background(), logs); err != nil {
		return err
	}
	channelIds := types.NewSet[int]()
	for _, log := range logs {
		if log.ChannelId != 0 {
			channelIds.Add(log.ChannelId)
		}
	}

	if channelIds.Len() > 0 {
		var channels []struct {
			Id   int    `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if common.MemoryCacheEnabled {
			// Cache get channel
			for _, channelId := range channelIds.Items() {
				if cacheChannel, err := CacheGetChannel(channelId); err == nil {
					channels = append(channels, struct {
						Id   int    `gorm:"column:id"`
						Name string `gorm:"column:name"`
					}{
						Id:   channelId,
						Name: cacheChannel.Name,
					})
				}
			}
		} else {
			// Bulk query channels from DB
			if err := DB.Table("channels").Select("id, name").Where("id IN ?", channelIds.Items()).Find(&channels).Error; err != nil {
				return err
			}
		}
		channelMap := make(map[int]string, len(channels))
		for _, channel := range channels {
			channelMap[channel.Id] = channel.Name
		}
		for i := range logs {
			logs[i].ChannelName = channelMap[logs[i].ChannelId]
		}
	}

	return nil
}

const logSearchCountLimit = 10000

func (f LogFilter) forUser(tx *gorm.DB, userID int) (*gorm.DB, error) {
	f.Source = LogSourceUsage
	if !f.AdminSelfView {
		tx = withoutLegacyAuditLogs(tx)
	}
	return f.apply(tx.Where("logs.user_id = ?", userID))
}

func GetUserLogs(userId int, filter LogFilter, startIdx int, num int) (logs []*Log, total int64, err error) {
	filter, err = filter.withRetries(context.Background(), userId)
	if err != nil {
		return nil, 0, err
	}
	tx, err := filter.forUser(LOG_DB, userId)
	if err != nil {
		return nil, 0, err
	}
	err = tx.Model(&Log{}).Limit(logSearchCountLimit).Count(&total).Error
	if err != nil {
		common.SysError(common.LogText("failed to count user logs: %s", err.Error()))
		return nil, 0, common.NewMessage("Failed to query logs")
	}
	order := "logs.created_at desc, logs.id desc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("logs.")
	}
	err = tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error
	if err != nil {
		common.SysError(common.LogText("failed to search user logs: %s", err.Error()))
		return nil, 0, common.NewMessage("Failed to query logs")
	}

	filter.retries.annotate(logs)
	if filter.Live {
		keyLiveLogs(logs)
	}
	formatUserLogs(logs, startIdx)
	return logs, total, resolveLogUsers(logs, false)
}

// StreamAllLogs calls fn with each log matching filter, newest first, as an
// administrator with role sees it. Rows are read one at a time, so an export
// never holds the whole result in memory.
func StreamAllLogs(ctx context.Context, filter LogFilter, role int, fn func(*Log) error) error {
	var err error
	filter, err = filter.withRetries(ctx, 0)
	if err != nil {
		return err
	}
	tx, err := filter.apply(LOG_DB.WithContext(ctx))
	if err != nil {
		return err
	}
	visibility := logOtherVisibilityAdmin
	if role >= common.RoleRootUser {
		visibility = logOtherVisibilityRoot
	}
	// Resolve names before opening the log cursor. The main and log database
	// can share a single connection, so a lookup inside the cursor would wait
	// for the very connection held by that cursor.
	var userIDs []int
	if err := tx.Session(&gorm.Session{}).Model(&Log{}).Distinct("user_id").
		Where("logs.user_id > ?", 0).Pluck("user_id", &userIDs).Error; err != nil {
		return err
	}
	users, err := currentLogUsers(ctx, userIDs, false)
	if err != nil {
		return err
	}
	var channelIDs []int
	if err := tx.Session(&gorm.Session{}).Model(&Log{}).Distinct("channel_id").
		Where("logs.channel_id <> ?", 0).Pluck("channel_id", &channelIDs).Error; err != nil {
		return err
	}
	channelNames := make(map[int]string, len(channelIDs))
	for start := 0; start < len(channelIDs); start += 500 {
		var channels []struct {
			Id   int
			Name string
		}
		if err := DB.WithContext(ctx).Model(&Channel{}).Select("id", "name").
			Where("id IN ?", channelIDs[start:min(start+500, len(channelIDs))]).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			channelNames[channel.Id] = channel.Name
		}
	}
	emitBatch := func(rows []*Log) error {
		if err := AttachSub2APIBills(ctx, rows); err != nil {
			return err
		}
		for _, log := range rows {
			log.useCurrentUser(users)
			log.ChannelName = channelNames[log.ChannelId]
			log.Other = formatLogOtherJSON(log.Other, visibility)
			if err := fn(log); err != nil {
				return err
			}
		}
		return nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// Historical ClickHouse rows may have identical/zero IDs. Preserve the
		// original cursor and tie ordering; invoices use the separate main DB.
		batch := make([]*Log, 0, 300)
		err := streamLogs(tx, clickHouseLogOrder("logs."), func(log *Log) error {
			batch = append(batch, log)
			if len(batch) < 300 {
				return nil
			}
			if err := emitBatch(batch); err != nil {
				return err
			}
			batch = batch[:0]
			return nil
		})
		if err != nil {
			return err
		}
		return emitBatch(batch)
	}
	// Release each bounded source batch before querying main-DB invoices. This
	// also works when the stores share SQLite's single connection.
	var before *Log
	for {
		batchQuery := tx.Session(&gorm.Session{})
		if before != nil {
			batchQuery = batchQuery.Where("logs.created_at < ? OR (logs.created_at = ? AND logs.id < ?)", before.CreatedAt, before.CreatedAt, before.Id)
		}
		var rows []*Log
		if err := batchQuery.Order("logs.created_at desc, logs.id desc").Limit(300).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		before = rows[len(rows)-1]
		if err := emitBatch(rows); err != nil {
			return err
		}
	}
}

// StreamUserLogs is StreamAllLogs for one user's own logs, shaped like the
// user log list.
func StreamUserLogs(ctx context.Context, userId int, filter LogFilter, fn func(*Log) error) error {
	var err error
	filter, err = filter.withRetries(ctx, userId)
	if err != nil {
		return err
	}
	tx, err := filter.forUser(LOG_DB.WithContext(ctx), userId)
	if err != nil {
		return err
	}
	users, err := currentLogUsers(ctx, []int{userId}, false)
	if err != nil {
		return err
	}
	count := 0
	return streamLogs(tx, "logs.id desc", func(log *Log) error {
		count++
		log.Id = count
		log.useCurrentUser(users)
		log.ChannelName = ""
		log.UpstreamRequestId = ""
		log.Other = formatLogOtherJSON(log.Other, logOtherVisibilityUser)
		return fn(log)
	})
}

func streamLogs(tx *gorm.DB, order string, fn func(*Log) error) error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("logs.")
	}
	rows, err := tx.Model(&Log{}).Order(order).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var log Log
		if err := LOG_DB.ScanRows(rows, &log); err != nil {
			return err
		}
		if err := fn(&log); err != nil {
			return err
		}
	}
	return rows.Err()
}

type Stat struct {
	LiveTotal *int64            `json:"live_total,omitempty" gorm:"-"`
	Quota     int               `json:"quota"`
	Rpm       int               `json:"rpm"`
	Tpm       int               `json:"tpm"`
	Finance   *DashboardFinance `json:"finance,omitempty" gorm:"-"`
}

// SumUsedQuota uses the same search as the log list. Rates keep their rolling
// sixty-second window; quota and finance use the exact selected time range.
func SumUsedQuota(ctx context.Context, filter LogFilter, userID *int, includeFinance bool) (Stat, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stat := Stat{}
	owner := 0
	if userID != nil {
		owner = *userID
	}
	var err error
	filter, err = filter.withRetries(ctx, owner)
	if err != nil {
		return stat, err
	}
	consumeTypes := []int{LogTypeConsume}
	if filter.Source == LogSourceTest {
		consumeTypes = append(consumeTypes, LogTypeTestConsume)
	}
	base := LOG_DB.WithContext(ctx).Model(&Log{}).Where("logs.type IN ?", consumeTypes)
	if userID != nil {
		base = base.Where("logs.user_id = ?", *userID)
	}
	tx, err := filter.apply(base.Session(&gorm.Session{}))
	if err != nil {
		return stat, err
	}
	if includeFinance && filter.Source != LogSourceTest {
		stat, err = logFinanceStat(tx)
		if err == nil {
			err = applySub2APIBillStat(ctx, tx, &stat, filter.StartTimestamp, filter.EndTimestamp)
		}
		if err == nil && Sub2APIStorageReady() {
			failed := LOG_DB.WithContext(ctx).Model(&Log{}).Where("logs.type = ?", LogTypeError)
			if userID != nil {
				failed = failed.Where("logs.user_id = ?", *userID)
			}
			failed, err = filter.apply(failed)
			if err == nil {
				err = applySub2APIBillStat(ctx, failed, &stat, filter.StartTimestamp, filter.EndTimestamp)
			}
		}
	} else {
		err = tx.Select("COALESCE(sum(quota), 0) quota").Scan(&stat).Error
	}
	if err != nil {
		common.SysError(common.LogText("failed to query log stat: %s", err.Error()))
		return Stat{}, common.NewMessage("Failed to query statistics")
	}
	rateFilter := filter
	rateFilter.EndTimestamp = common.GetTimestamp()
	rateFilter.StartTimestamp = rateFilter.EndTimestamp - 60
	rateQuery, err := rateFilter.apply(base.Session(&gorm.Session{}))
	if err != nil {
		return Stat{}, err
	}
	var rates struct {
		Rpm int
		Tpm int
	}
	err = rateQuery.Select("count(*) rpm, COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0) tpm").Scan(&rates).Error
	if err != nil {
		common.SysError(common.LogText("failed to query rpm/tpm stat: %s", err.Error()))
		return Stat{}, common.NewMessage("Failed to query statistics")
	}
	if filter.Live {
		// Rates and spending cover consumption only. A live table includes
		// errors and other visible types as well, just like its snapshot.
		q := LOG_DB.WithContext(ctx).Model(&Log{})
		if userID == nil {
			q, err = filter.apply(q)
		} else {
			q, err = filter.forUser(q, *userID)
		}
		if err != nil {
			return Stat{}, err
		}
		var count int64
		if err := q.Count(&count).Error; err != nil {
			return Stat{}, err
		}
		stat.LiveTotal = &count
	}
	stat.Rpm, stat.Tpm = rates.Rpm, rates.Tpm
	return stat, nil
}

func SumUsedToken(logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string) (token int) {
	tx := applyLogSourceFilter(LOG_DB.Table("logs")).Select("COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0)")
	if username != "" {
		tx = tx.Where("username = ?", username)
	}
	if tokenName != "" {
		tx = tx.Where("token_name = ?", tokenName)
	}
	if startTimestamp != 0 {
		tx = tx.Where("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("created_at <= ?", endTimestamp)
	}
	if modelName != "" {
		tx = tx.Where("model_name = ?", modelName)
	}
	tx.Where("type = ?", LogTypeConsume).Scan(&token)
	return token
}

func CountOldLog(ctx context.Context, targetTimestamp int64) (int64, error) {
	var total int64
	if err := LOG_DB.WithContext(ctx).Model(&Log{}).Where("created_at < ?", targetTimestamp).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func DeleteOldLogBatch(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if nil != ctx.Err() {
		return 0, ctx.Err()
	}

	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// ClickHouse DELETE is a heavy mutation that rewrites data parts, so
		// per-batch mutations would be pathologically slow. Remove all matching
		// rows in a single synchronous mutation regardless of limit; the reported
		// count lets the caller's progress loop complete in one pass.
		total, err := CountOldLog(ctx, targetTimestamp)
		if err != nil {
			return 0, err
		}
		if total == 0 {
			return 0, nil
		}
		if err := LOG_DB.WithContext(ctx).Exec(
			"ALTER TABLE logs DELETE WHERE created_at < ? SETTINGS mutations_sync = 1",
			targetTimestamp,
		).Error; err != nil {
			return 0, err
		}
		return total, nil
	}

	result := LOG_DB.WithContext(ctx).Where("created_at < ?", targetTimestamp).Limit(limit).Delete(&Log{})
	if nil != result.Error {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
