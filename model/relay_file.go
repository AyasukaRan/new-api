package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// RelayFile records one file that a user uploaded through the gateway to an
// upstream provider.
//
// The row is not a cache: it is the authorization boundary. An upstream file id
// is only unique inside the provider account that owns the channel key, and
// every gateway user shares that one account, so relaying a bare id would let
// anyone read, download, or delete another user's batch input by guessing it.
// Every read path therefore resolves the id through this table scoped to the
// caller, and the row is also what pins the follow-up request to the channel
// that actually holds the bytes.
type RelayFile struct {
	Id int `json:"id"`
	// FileId is the provider-issued id. It is unique per owner rather than
	// globally: two channels may legitimately hand out the same id.
	FileId    string `json:"file_id" gorm:"type:varchar(191);uniqueIndex:idx_relay_files_user_file,priority:2"`
	UserId    int    `json:"user_id" gorm:"uniqueIndex:idx_relay_files_user_file,priority:1"`
	ChannelId int    `json:"channel_id" gorm:"index"`
	// Model is the model named inside the uploaded batch file. It selects the
	// channel on upload and prices the batch that later consumes the file.
	Model     string `json:"model" gorm:"type:varchar(191)"`
	Purpose   string `json:"purpose" gorm:"type:varchar(32)"`
	Filename  string `json:"filename" gorm:"type:varchar(255)"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
}

// ErrRelayFileNotFound distinguishes "this caller owns no such file" from a
// database failure. Both are answered to the client as a 404 so the response
// cannot be used to probe which ids exist on the shared provider account.
var ErrRelayFileNotFound = errors.New("file not found")

func (file *RelayFile) Insert() error {
	file.CreatedAt = common.GetTimestamp()
	// A provider may reissue an id after the original was deleted upstream, so
	// the newer upload has to win rather than collide with the stale row.
	return DB.Where("user_id = ? AND file_id = ?", file.UserId, file.FileId).
		Assign(map[string]any{
			"channel_id": file.ChannelId,
			"model":      file.Model,
			"purpose":    file.Purpose,
			"filename":   file.Filename,
			"bytes":      file.Bytes,
			"created_at": file.CreatedAt,
		}).
		FirstOrCreate(file).Error
}

// GetRelayFile resolves a file the caller owns. It never matches on file id
// alone.
func GetRelayFile(userId int, fileId string) (*RelayFile, error) {
	if userId == 0 || fileId == "" {
		return nil, ErrRelayFileNotFound
	}
	var file RelayFile
	err := DB.Where("user_id = ? AND file_id = ?", userId, fileId).First(&file).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRelayFileNotFound
	}
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// ListRelayFiles returns the caller's own files, newest first. The provider's
// own list endpoint is deliberately not relayed: it would enumerate every
// gateway user's files that share the channel key.
func ListRelayFiles(userId int, purpose string, limit int) ([]*RelayFile, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := DB.Where("user_id = ?", userId)
	if purpose != "" {
		query = query.Where("purpose = ?", purpose)
	}
	var files []*RelayFile
	err := query.Order("created_at desc, id desc").Limit(limit).Find(&files).Error
	return files, err
}

func DeleteRelayFile(userId int, fileId string) error {
	return DB.Where("user_id = ? AND file_id = ?", userId, fileId).Delete(&RelayFile{}).Error
}
