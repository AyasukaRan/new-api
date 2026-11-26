package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
)

const personalDashboardSetting = "personal_dashboards"

var ErrDashboardRevisionConflict = errors.New("Dashboard changed in another tab. Reload before saving.")

// SQLite must acquire its writer before reading, since it cannot upgrade a
// competing deferred read transaction to the row lock used by MySQL/Postgres.
func lockUserSettings(tx *gorm.DB, userID int) (*User, error) {
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("setting", gorm.Expr("setting")).Error; err != nil {
			return nil, err
		}
	}
	var user User
	err := lockForUpdate(tx).Select("id", "role", "status", "setting").First(&user, userID).Error
	return &user, err
}

func GetPersonalDashboard(userID int) (dto.DashboardConfigResponse, error) {
	result := dto.DashboardConfigResponse{}
	if userID <= 0 {
		return result, gorm.ErrRecordNotFound
	}
	var user User
	if err := DB.Select("id", "role", "status", "setting").First(&user, userID).Error; err != nil {
		return result, err
	}
	if user.Status != common.UserStatusEnabled {
		return result, gorm.ErrRecordNotFound
	}
	settings := map[string]common.RawMessage{}
	if user.Setting != "" {
		if err := common.UnmarshalJsonStr(user.Setting, &settings); err != nil {
			return result, err
		}
	}
	if raw := settings[personalDashboardSetting]; len(raw) > 0 {
		if err := common.Unmarshal(raw, &result); err != nil {
			return result, err
		}
	}
	result.Config.ForUser(user.Role >= common.RoleAdminUser)
	return result, nil
}

func SavePersonalDashboard(userID int, expectedRevision int64, config *dto.DashboardConfig) (dto.DashboardConfigResponse, error) {
	result := dto.DashboardConfigResponse{}
	if userID <= 0 {
		return result, gorm.ErrRecordNotFound
	}
	if expectedRevision < 0 || expectedRevision >= 9007199254740991 {
		return result, dto.ErrDashboardConfigInvalid
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockUserSettings(tx, userID)
		if err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return gorm.ErrRecordNotFound
		}
		if err := config.Validate(user.Role >= common.RoleAdminUser); err != nil {
			return err
		}
		settings := map[string]common.RawMessage{}
		if user.Setting != "" {
			if err := common.UnmarshalJsonStr(user.Setting, &settings); err != nil {
				return err
			}
		}
		if settings == nil {
			settings = make(map[string]common.RawMessage)
		}
		if raw := settings[personalDashboardSetting]; len(raw) > 0 {
			if err := common.Unmarshal(raw, &result); err != nil {
				return err
			}
		}
		if result.Revision != expectedRevision {
			return ErrDashboardRevisionConflict
		}
		result = dto.DashboardConfigResponse{Revision: expectedRevision + 1, Config: config}
		raw, err := common.Marshal(result)
		if err != nil {
			return err
		}
		if len(raw) > dto.MaxDashboardConfigBytes {
			return dto.ErrDashboardConfigInvalid
		}
		settings[personalDashboardSetting] = raw
		encoded, err := common.Marshal(settings)
		if err != nil {
			return err
		}
		if len(encoded) > 65535 {
			return dto.ErrDashboardConfigInvalid
		}
		// The JSON equality also protects against older processes writing settings
		// without participating in this feature's revision protocol.
		update := tx.Model(&User{}).Where("id = ? AND COALESCE(setting, '') = ?", userID, user.Setting).Update("setting", string(encoded))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDashboardRevisionConflict
		}
		return nil
	})
	if err != nil {
		return dto.DashboardConfigResponse{}, err
	}
	if err := PublishUserAuthCache(userID); err != nil {
		common.SysError(common.LogText("failed to refresh user settings cache after dashboard update"))
	}
	return result, nil
}
