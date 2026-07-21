package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func normalizeWebDAVMetadataPath(name string) string {
	if name == "" {
		return "/"
	}
	if name[0] != '/' {
		name = "/" + name
	}
	return path.Clean(name)
}

func webDAVMetadataPathHash(name string) string {
	sum := sha256.Sum256([]byte(normalizeWebDAVMetadataPath(name)))
	return hex.EncodeToString(sum[:])
}

func prepareWebDAVMetadata(metadata *model.WebDAVMetadata) {
	metadata.Path = normalizeWebDAVMetadataPath(metadata.Path)
	metadata.PathHash = webDAVMetadataPathHash(metadata.Path)
	metadata.ParentHash = webDAVMetadataPathHash(path.Dir(metadata.Path))
}

func UpsertWebDAVMetadata(ctx context.Context, metadata *model.WebDAVMetadata) error {
	prepareWebDAVMetadata(metadata)
	now := time.Now()
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
	}
	metadata.UpdatedAt = now
	return errors.WithStack(db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "path_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"path",
			"parent_hash",
			"mod_time",
			"create_time",
			"mod_time_nsec",
			"create_time_nsec",
			"has_mod_time",
			"has_create_time",
			"size",
			"is_dir",
			"object_id",
			"hash_type",
			"hash",
			"backend_mod_time_nsec",
			"content_hash_type",
			"content_hash",
			"dead_properties",
			"updated_at",
		}),
	}).Create(metadata).Error)
}

func GetWebDAVMetadata(ctx context.Context, name string) (*model.WebDAVMetadata, bool, error) {
	name = normalizeWebDAVMetadataPath(name)
	var metadata model.WebDAVMetadata
	err := db.WithContext(ctx).
		Where("path_hash = ? AND path = ?", webDAVMetadataPathHash(name), name).
		First(&metadata).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.WithStack(err)
	}
	return &metadata, true, nil
}

// GetWebDAVMetadataForDirectory returns metadata for name and its direct
// children. This matches the Depth: 1 PROPFIND pattern used by most clients.
func GetWebDAVMetadataForDirectory(ctx context.Context, name string) (map[string]model.WebDAVMetadata, error) {
	name = normalizeWebDAVMetadataPath(name)
	hash := webDAVMetadataPathHash(name)
	var candidates []model.WebDAVMetadata
	if err := db.WithContext(ctx).
		Where("path_hash = ? OR parent_hash = ?", hash, hash).
		Find(&candidates).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	result := make(map[string]model.WebDAVMetadata, len(candidates))
	for _, metadata := range candidates {
		if metadata.Path == name || path.Dir(metadata.Path) == name {
			result[metadata.Path] = metadata
		}
	}
	return result, nil
}

func GetWebDAVMetadataTree(ctx context.Context, name string) (map[string]model.WebDAVMetadata, error) {
	records, err := getWebDAVMetadataTree(db.WithContext(ctx), name)
	if err != nil {
		return nil, err
	}
	result := make(map[string]model.WebDAVMetadata, len(records))
	for _, metadata := range records {
		result[metadata.Path] = metadata
	}
	return result, nil
}

func DeleteWebDAVMetadata(ctx context.Context, name string) error {
	name = normalizeWebDAVMetadataPath(name)
	return errors.WithStack(db.WithContext(ctx).
		Where("path_hash = ? AND path = ?", webDAVMetadataPathHash(name), name).
		Delete(&model.WebDAVMetadata{}).Error)
}

func DeleteWebDAVMetadataTree(ctx context.Context, name string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return deleteWebDAVMetadataTree(tx, name)
	})
}

// CopyWebDAVMetadataTree replaces destination metadata with copies of source
// metadata. Object IDs are cleared because copies commonly receive new IDs.
func CopyWebDAVMetadataTree(ctx context.Context, src, dst string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err := getWebDAVMetadataTree(tx, src)
		if err != nil {
			return err
		}
		if err = deleteWebDAVMetadataTree(tx, dst); err != nil {
			return err
		}
		copied := rewriteWebDAVMetadataTree(source, src, dst)
		return upsertWebDAVMetadataBatch(tx, copied)
	})
}

// MoveWebDAVMetadataTree replaces destination metadata and rewrites source
// paths atomically inside the metadata database.
func MoveWebDAVMetadataTree(ctx context.Context, src, dst string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err := getWebDAVMetadataTree(tx, src)
		if err != nil {
			return err
		}
		if err = deleteWebDAVMetadataTree(tx, dst); err != nil {
			return err
		}
		if err = deleteWebDAVMetadataRecords(tx, source); err != nil {
			return err
		}
		moved := rewriteWebDAVMetadataTree(source, src, dst)
		return upsertWebDAVMetadataBatch(tx, moved)
	})
}

func getWebDAVMetadataTree(tx *gorm.DB, name string) ([]model.WebDAVMetadata, error) {
	name = normalizeWebDAVMetadataPath(name)
	like := name + "/%"
	if name == "/" {
		like = "/%"
	}
	var candidates []model.WebDAVMetadata
	if err := tx.Where("path = ? OR path LIKE ?", name, like).Find(&candidates).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	result := make([]model.WebDAVMetadata, 0, len(candidates))
	for _, metadata := range candidates {
		if webDAVMetadataPathWithin(metadata.Path, name) {
			result = append(result, metadata)
		}
	}
	return result, nil
}

func webDAVMetadataPathWithin(candidate, root string) bool {
	candidate = normalizeWebDAVMetadataPath(candidate)
	root = normalizeWebDAVMetadataPath(root)
	if root == "/" {
		return strings.HasPrefix(candidate, "/")
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func deleteWebDAVMetadataTree(tx *gorm.DB, name string) error {
	records, err := getWebDAVMetadataTree(tx, name)
	if err != nil {
		return err
	}
	return deleteWebDAVMetadataRecords(tx, records)
}

func deleteWebDAVMetadataRecords(tx *gorm.DB, records []model.WebDAVMetadata) error {
	if len(records) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(records))
	for _, metadata := range records {
		hashes = append(hashes, metadata.PathHash)
	}
	return errors.WithStack(tx.Where("path_hash IN ?", hashes).Delete(&model.WebDAVMetadata{}).Error)
}

func rewriteWebDAVMetadataTree(records []model.WebDAVMetadata, src, dst string) []model.WebDAVMetadata {
	src = normalizeWebDAVMetadataPath(src)
	dst = normalizeWebDAVMetadataPath(dst)
	result := make([]model.WebDAVMetadata, 0, len(records))
	for _, metadata := range records {
		suffix := strings.TrimPrefix(metadata.Path, src)
		if src == "/" {
			suffix = metadata.Path
		}
		metadata.Path = normalizeWebDAVMetadataPath(dst + suffix)
		metadata.ObjectID = ""
		metadata.BackendModTimeNsec = 0
		metadata.CreatedAt = time.Time{}
		prepareWebDAVMetadata(&metadata)
		result = append(result, metadata)
	}
	return result
}

func upsertWebDAVMetadataBatch(tx *gorm.DB, records []model.WebDAVMetadata) error {
	if len(records) == 0 {
		return nil
	}
	now := time.Now()
	for i := range records {
		if records[i].CreatedAt.IsZero() {
			records[i].CreatedAt = now
		}
		records[i].UpdatedAt = now
	}
	return errors.WithStack(tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path_hash"}},
		UpdateAll: true,
	}).CreateInBatches(records, 100).Error)
}
