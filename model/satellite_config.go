package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Configuration is primary-owned. Persistent field names are used rather than
// public API JSON, whose secret redaction deliberately omits relay credentials.
// This payload must only travel through the authenticated private sync endpoint.
type SatelliteConfigTable struct {
	Name string                         `json:"name"`
	Rows []map[string]common.RawMessage `json:"rows"`
}

type SatelliteConfigSnapshot struct {
	Version    int                         `json:"version"`
	Digest     string                      `json:"digest"`
	Tables     []SatelliteConfigTable      `json:"tables"`
	Accounting []SatelliteAccountingAnchor `json:"accounting"`
}

type SatelliteReplicaState struct {
	ID           string `gorm:"primaryKey;size:64"`
	ConfigDigest string `gorm:"size:64"`
	LastSyncAt   int64
}

type satelliteConfigSpec struct {
	model any
	omit  []string
}

// Browser identities and sessions stay at the primary console; a satellite is
// an API ingress, not an independently writable account-management server.
func satelliteConfigSpecs() []satelliteConfigSpec {
	return []satelliteConfigSpec{
		{&User{}, []string{"password", "access_token", "access_token_created_at", "quota", "used_quota", "request_count", "last_login_at"}},
		{&Token{}, []string{"remain_quota", "used_quota", "accessed_time"}},
		{&Channel{}, []string{"used_quota"}},
		{&Option{}, nil}, {&Ability{}, nil}, {&Model{}, nil}, {&Vendor{}, nil},
		{&PrefillGroup{}, nil}, {&TaskPlugin{}, nil}, {&CasbinRule{}, nil}, {&AuthzRole{}, nil},
		{&SubscriptionPlan{}, nil}, {&UserSubscription{}, []string{"amount_used"}},
		{&Sub2APIAccount{}, []string{"credential", "proxy_credential", "inventory_json", "sync_state_json"}},
		{&Sub2APIKeyBinding{}, nil},
	}
}

func ExportSatelliteConfig(ctx context.Context, nodeID string) (*SatelliteConfigSnapshot, error) {
	snapshot := &SatelliteConfigSnapshot{Version: 1}
	isolation := sql.LevelRepeatableRead
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		isolation = sql.LevelSerializable
	}
	err := DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		for _, spec := range satelliteConfigSpecs() {
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(spec.model); err != nil {
				return err
			}
			rows := reflect.New(reflect.SliceOf(stmt.Schema.ModelType))
			query := tx.Unscoped()
			for _, field := range stmt.Schema.PrimaryFields {
				query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: field.DBName}})
			}
			if err := query.Find(rows.Interface()).Error; err != nil {
				return err
			}
			table := SatelliteConfigTable{Name: stmt.Schema.Table, Rows: make([]map[string]common.RawMessage, 0, rows.Elem().Len())}
			for i := range rows.Elem().Len() {
				if option, ok := rows.Elem().Index(i).Interface().(Option); ok {
					// The ingress needs relay configuration, never credentials for
					// browser login, payments or mail owned by the primary console.
					switch option.Key {
					case "SMTPToken", "ResendApiKey", "GitHubClientSecret", "LinuxDOClientSecret",
						"TelegramBotToken", "WeChatServerToken", "TurnstileSecretKey",
						"oidc.client_secret", "discord.client_secret", "telegram.client_secret",
						"EpayKey", "payment_setting.epay_merchant_private_key",
						"StripeApiSecret", "StripeWebhookSecret", "CreemApiKey", "CreemWebhookSecret",
						"WaffoApiKey", "WaffoPrivateKey", "WaffoSandboxApiKey", "WaffoSandboxPrivateKey", "WaffoPancakePrivateKey":
						continue
					}
				}
				record := make(map[string]common.RawMessage)
				for _, field := range stmt.Schema.Fields {
					if field.DBName == "" || slices.Contains(spec.omit, field.DBName) {
						continue
					}
					value, _ := field.ValueOf(ctx, rows.Elem().Index(i))
					data, err := common.Marshal(value)
					if err != nil {
						return fmt.Errorf("encode satellite configuration %s.%s", table.Name, field.DBName)
					}
					record[field.DBName] = data
				}
				table.Rows = append(table.Rows, record)
			}
			snapshot.Tables = append(snapshot.Tables, table)
		}
		var err error
		snapshot.Accounting, err = ExportSatelliteAccountingAnchors(tx, nodeID)
		return err
	}, &sql.TxOptions{Isolation: isolation})
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(snapshot.Tables)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	snapshot.Digest = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func ApplySatelliteConfig(ctx context.Context, snapshot *SatelliteConfigSnapshot) (bool, error) {
	if !common.IsSatellite() || snapshot == nil || snapshot.Version != 1 {
		return false, errors.New("invalid satellite configuration")
	}
	data, err := common.Marshal(snapshot.Tables)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != snapshot.Digest {
		return false, errors.New("satellite configuration digest mismatch")
	}
	var state SatelliteReplicaState
	err = DB.WithContext(ctx).First(&state, "id = ?", "primary").Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	changed := state.ConfigDigest != snapshot.Digest
	var previousTokens []Token
	// Fence old token metadata before a changed snapshot can revoke or replace it.
	if changed {
		if err := DB.Select("key").Unscoped().Find(&previousTokens).Error; err != nil {
			return false, err
		}
		for _, token := range previousTokens {
			if err := invalidateTokenCacheForMutation(token.Key); err != nil {
				return false, err
			}
		}
	}
	err = DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		specs := satelliteConfigSpecs()
		if len(snapshot.Tables) != len(specs) {
			return errors.New("incomplete satellite configuration")
		}
		for index, spec := range specs {
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(spec.model); err != nil {
				return err
			}
			table := snapshot.Tables[index]
			if table.Name != stmt.Schema.Table {
				return errors.New("unknown satellite configuration table")
			}
			if !changed {
				continue
			}
			var primaryKeys []clause.Column
			for _, field := range stmt.Schema.PrimaryFields {
				primaryKeys = append(primaryKeys, clause.Column{Name: field.DBName})
			}
			if len(primaryKeys) == 0 {
				return fmt.Errorf("unsupported satellite configuration key: %s", table.Name)
			}
			preserveAccounting := false
			switch spec.model.(type) {
			case *User, *Token, *Channel, *UserSubscription:
				preserveAccounting = true
			}
			if !preserveAccounting {
				if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Unscoped().Delete(spec.model).Error; err != nil {
					return err
				}
			}
			ids := make([]any, 0, len(table.Rows))
			for _, row := range table.Rows {
				value := reflect.New(stmt.Schema.ModelType)
				var columns []string
				exactValues := make(map[string]any)
				for _, field := range stmt.Schema.Fields {
					if field.DBName == "" || slices.Contains(spec.omit, field.DBName) {
						continue
					}
					raw, ok := row[field.DBName]
					if !ok {
						return fmt.Errorf("incomplete satellite configuration field: %s.%s", table.Name, field.DBName)
					}
					fieldValue := reflect.New(field.FieldType)
					if err := common.Unmarshal(raw, fieldValue.Interface()); err != nil {
						return fmt.Errorf("invalid satellite configuration field: %s.%s", table.Name, field.DBName)
					}
					if err := field.Set(ctx, value.Elem(), fieldValue.Elem().Interface()); err != nil {
						return err
					}
					columns = append(columns, field.DBName)
					exactValues[field.DBName] = fieldValue.Elem().Interface()
				}
				id, zero := stmt.Schema.PrimaryFields[0].ValueOf(ctx, value.Elem())
				if zero {
					return fmt.Errorf("missing satellite configuration identity: %s", table.Name)
				}
				ids = append(ids, id)
				// Insert complete typed rows (including required defaults), but never
				// replace accounting counters on conflict. Hooks must not rewrite timestamps.
				updates := slices.DeleteFunc(slices.Clone(columns), func(column string) bool { return column == primaryKeys[0].Name })
				query := tx.Session(&gorm.Session{SkipHooks: true}).Unscoped()
				if preserveAccounting {
					query = query.Clauses(clause.OnConflict{Columns: primaryKeys, DoUpdates: clause.AssignmentColumns(updates)})
				}
				if err := query.Create(value.Interface()).Error; err != nil {
					return err
				}
				// GORM Create applies struct defaults to zero values. Restore the exact
				// snapshot values (false, 0 and empty included) before the transaction commits.
				// Updates assigns map values back into its model. Keep the shared
				// schema prototype empty for the later missing-identity query.
				exact := tx.Session(&gorm.Session{SkipHooks: true}).Unscoped().Model(reflect.New(stmt.Schema.ModelType).Interface())
				for _, key := range primaryKeys {
					exact = exact.Where(clause.Eq{Column: key, Value: exactValues[key.Name]})
				}
				if err := exact.Updates(exactValues).Error; err != nil {
					return err
				}
			}
			// A removed primary identity remains a disabled local tombstone until all
			// in-flight charges settle; never delete its accounting history on refresh.
			if !preserveAccounting {
				continue
			}
			query := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped()
			if len(ids) > 0 {
				query = query.Where(clause.Not(clause.IN{Column: primaryKeys[0], Values: ids}))
			}
			var status any
			switch spec.model.(type) {
			case *User:
				// A hard-deleted primary identity has no newer version to copy.
				// Advance it once so an old cache fill cannot restore access.
				if err := query.Model(spec.model).Where("status <> ?", common.UserStatusDisabled).Updates(map[string]any{
					"status": common.UserStatusDisabled, "auth_version": gorm.Expr("auth_version + 1"),
				}).Error; err != nil {
					return err
				}
				continue
			case *Token:
				status = common.TokenStatusDisabled
			case *Channel:
				status = common.ChannelStatusManuallyDisabled
			case *UserSubscription:
				status = "cancelled"
			}
			if err := query.Model(spec.model).Update("status", status).Error; err != nil {
				return err
			}
		}
		if err := ReconcileSatelliteAccountingAnchors(tx, snapshot.Accounting); err != nil {
			return err
		}
		state = SatelliteReplicaState{ID: "primary", ConfigDigest: snapshot.Digest, LastSyncAt: common.GetTimestamp()}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&state).Error
	})
	if err != nil {
		return false, err
	}
	{
		var tokens []Token
		if err := DB.Unscoped().Select("key").Find(&tokens).Error; err != nil {
			return false, err
		}
		for _, token := range append(previousTokens, tokens...) {
			if err := invalidateTokenCacheForMutation(token.Key); err != nil {
				return false, err
			}
		}
		var users []User
		if err := DB.Unscoped().Select("id", "auth_version").Find(&users).Error; err != nil {
			return false, err
		}
		for _, user := range users {
			if err := publishCommittedUserAuthVersion(user.Id, user.AuthVersion); err != nil {
				return false, err
			}
			if err := invalidateUserCache(user.Id); err != nil {
				return false, err
			}
		}
		// Includes disabled tombstones, so removed bindings cannot survive in
		// the process cache after a configuration refresh.
		var channelIDs []int
		if err := DB.Model(&Channel{}).Pluck("id", &channelIDs).Error; err != nil {
			return false, err
		}
		for _, channelID := range channelIDs {
			InvalidateSub2APIBindings(channelID)
		}
	}
	return changed, nil
}

func SatelliteHasConfiguration(ctx context.Context) bool {
	var state SatelliteReplicaState
	return DB.WithContext(ctx).First(&state, "id = ?", "primary").Error == nil && state.ConfigDigest != ""
}
