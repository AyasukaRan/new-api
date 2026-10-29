package model

import (
	"context"
	"io"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	Id                int    `json:"id" gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int    `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64  `json:"created_at" gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int    `json:"type" gorm:"index:idx_created_at_type"`
	Content           string `json:"content"`
	Username          string `json:"username" gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string `json:"token_name" gorm:"index;default:''"`
	ModelName         string `json:"model_name" gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int    `json:"quota" gorm:"default:0"`
	PromptTokens      int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens  int    `json:"completion_tokens" gorm:"default:0"`
	UseTime           int    `json:"use_time" gorm:"default:0"`
	IsStream          bool   `json:"is_stream"`
	ChannelId         int    `json:"channel" gorm:"index"`
	ChannelName       string `json:"channel_name" gorm:"->"`
	TokenId           int    `json:"token_id" gorm:"default:0;index"`
	Group             string `json:"group" gorm:"index"`
	Ip                string `json:"ip" gorm:"index;default:''"`
	RequestId         string `json:"request_id,omitempty" gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string `json:"other"`
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
	return LOG_DB.Create(log).Error
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
		logs[i].ChannelName = ""
		logs[i].Other = formatLogOtherJSON(logs[i].Other, logOtherVisibilityUser)
	}
	assignDisplayLogIds(logs, startIdx)
}

// FormatAdminLogs removes root-only diagnostics while retaining operational
// admin_info. Root callers must not pass their results through this formatter.
func FormatAdminLogs(logs []*Log) {
	for i := range logs {
		logs[i].Other = formatLogOtherJSON(logs[i].Other, logOtherVisibilityAdmin)
	}
}

// FormatRootLogs normalizes legacy metadata into the current scoped shape
// without removing root-only diagnostics.
func FormatRootLogs(logs []*Log) {
	for i := range logs {
		logs[i].Other = formatLogOtherJSON(logs[i].Other, logOtherVisibilityRoot)
	}
}

func GetLogByTokenId(tokenId int) (logs []*Log, err error) {
	order := "id desc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("")
	}
	err = applyLogSourceFilter(LOG_DB.Model(&Log{})).Where("token_id = ?", tokenId).Order(order).Limit(common.MaxRecentItems).Find(&logs).Error
	formatUserLogs(logs, 0)
	return logs, err
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
	// 判断是否需要记录 IP：默认记录，读不到设置时按不记录处理
	needRecordIp := false
	if settingMap, err := GetUserSetting(userId, false); err == nil {
		needRecordIp = settingMap.ShouldRecordIp()
	}
	log := &Log{
		UserId:           userId,
		Username:         username,
		CreatedAt:        common.GetTimestamp(),
		Type:             LogTypeError,
		Content:          content,
		PromptTokens:     0,
		CompletionTokens: 0,
		TokenName:        tokenName,
		ModelName:        modelName,
		Quota:            0,
		ChannelId:        channelId,
		TokenId:          tokenId,
		UseTime:          useTimeSeconds,
		IsStream:         isStream,
		Group:            group,
		Ip: func() string {
			if needRecordIp {
				return c.ClientIP()
			}
			return ""
		}(),
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
	if other != nil && other.adminInfo["cost"] != nil {
		other.SetRoot("node_name", common.NodeName)
	}
	attachRequestBody(c, other)
	content := other.setContent(params.Content)
	otherStr := other.JSONString()
	// IP capture follows the user's preference and fails closed on lookup errors.
	needRecordIp := false
	if settingMap, err := GetUserSetting(userId, false); err == nil {
		needRecordIp = settingMap.ShouldRecordIp()
	}
	log := &Log{
		UserId:           userId,
		Username:         username,
		CreatedAt:        createdAt,
		Type:             logType,
		Content:          content,
		PromptTokens:     params.PromptTokens,
		CompletionTokens: params.CompletionTokens,
		TokenName:        params.TokenName,
		ModelName:        params.ModelName,
		Quota:            params.Quota,
		ChannelId:        params.ChannelId,
		TokenId:          params.TokenId,
		UseTime:          params.UseTimeSeconds,
		IsStream:         params.IsStream,
		Group:            params.Group,
		Ip: func() string {
			if needRecordIp {
				return c.ClientIP()
			}
			return ""
		}(),
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
// Username and Channel are administrator filters.
type LogFilter struct {
	Source            LogSource
	Type              int
	StartTimestamp    int64
	EndTimestamp      int64
	ModelName         string
	Username          string
	TokenName         string
	Channel           int
	Group             string
	RequestId         string
	UpstreamRequestId string
}

func (f LogFilter) apply(tx *gorm.DB) (*gorm.DB, error) {
	var err error
	tx = applyLogSourceFilter(tx, f.Source)
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
	if tx, err = applyExplicitLogTextFilter(tx, "logs.username", f.Username); err != nil {
		return nil, err
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

func GetAllLogs(filter LogFilter, startIdx int, num int) (logs []*Log, total int64, err error) {
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
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assignDisplayLogIds(logs, startIdx)
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
			if err = DB.Table("channels").Select("id, name").Where("id IN ?", channelIds.Items()).Find(&channels).Error; err != nil {
				return logs, total, err
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

	return logs, total, err
}

const logSearchCountLimit = 10000

func GetUserLogs(userId int, filter LogFilter, startIdx int, num int) (logs []*Log, total int64, err error) {
	filter.Source = LogSourceUsage
	tx, err := filter.apply(LOG_DB.Where("logs.user_id = ?", userId))
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

	formatUserLogs(logs, startIdx)
	return logs, total, err
}

// StreamAllLogs calls fn with each log matching filter, newest first, as an
// administrator with role sees it. Rows are read one at a time, so an export
// never holds the whole result in memory.
func StreamAllLogs(ctx context.Context, filter LogFilter, role int, fn func(*Log) error) error {
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
	return streamLogs(tx, "logs.created_at desc, logs.id desc", func(log *Log) error {
		log.ChannelName = channelNames[log.ChannelId]
		log.Other = formatLogOtherJSON(log.Other, visibility)
		return fn(log)
	})
}

// StreamUserLogs is StreamAllLogs for one user's own logs, shaped like the
// user log list.
func StreamUserLogs(ctx context.Context, userId int, filter LogFilter, fn func(*Log) error) error {
	filter.Source = LogSourceUsage
	tx, err := filter.apply(LOG_DB.WithContext(ctx).Where("logs.user_id = ?", userId))
	if err != nil {
		return err
	}
	count := 0
	return streamLogs(tx, "logs.id desc", func(log *Log) error {
		count++
		log.Id = count
		log.ChannelName = ""
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
	Quota   int               `json:"quota"`
	Rpm     int               `json:"rpm"`
	Tpm     int               `json:"tpm"`
	Finance *DashboardFinance `json:"finance,omitempty" gorm:"-"`
}

// SumUsedQuota uses the same search as the log list. Rates keep their rolling
// sixty-second window; quota and finance use the exact selected time range.
func SumUsedQuota(ctx context.Context, filter LogFilter, userID *int, includeFinance bool) (Stat, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stat := Stat{}
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
		stat.Finance = &DashboardFinance{}
		var count int64
		// Keep memory bounded and read quota/cost from the same cursor snapshot.
		// Rows without a cost candidate need not transfer captured request bodies.
		err = streamLogs(tx.Select("quota, CASE WHEN other LIKE ? THEN other ELSE '' END AS other", `%"cost"%`), "logs.created_at desc, logs.id desc", func(log *Log) error {
			if (log.Quota > 0 && stat.Quota > math.MaxInt-log.Quota) || (log.Quota < 0 && stat.Quota < math.MinInt-log.Quota) {
				return common.NewMessage("Failed to query statistics")
			}
			stat.Quota += log.Quota
			count++
			var metadata struct {
				AdminInfo struct {
					Cost *dashboardCostSnapshot `json:"cost"`
				} `json:"admin_info"`
			}
			if common.UnmarshalJsonStr(log.Other, &metadata) != nil {
				return nil
			}
			if finance, valid := recordedLogFinance(log.Quota, metadata.AdminInfo.Cost); valid {
				stat.Finance.add(finance)
			}
			return nil
		})
		stat.Finance.UnrecordedCount = count - stat.Finance.RecordedCount
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
