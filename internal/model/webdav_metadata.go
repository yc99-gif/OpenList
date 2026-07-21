package model

import "time"

// WebDAVMetadata stores WebDAV-facing timestamps, plaintext checksums and dead
// properties independently from backing storage that may not preserve them.
type WebDAVMetadata struct {
	PathHash           string `gorm:"primaryKey;size:64"`
	Path               string `gorm:"type:text;not null"`
	ParentHash         string `gorm:"index;size:64;not null"`
	ModTime            int64
	CreateTime         int64
	ModTimeNsec        int64
	CreateTimeNsec     int64
	HasModTime         bool `gorm:"not null;default:false"`
	HasCreateTime      bool `gorm:"not null;default:false"`
	Size               int64
	IsDir              bool
	ObjectID           string `gorm:"type:text"`
	HashType           string `gorm:"size:16"`
	Hash               string `gorm:"type:text"`
	BackendModTimeNsec int64
	ContentHashType    string `gorm:"size:16"`
	ContentHash        string `gorm:"type:text"`
	DeadProperties     string `gorm:"type:text"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (WebDAVMetadata) TableName() string {
	return "webdav_metadata"
}
