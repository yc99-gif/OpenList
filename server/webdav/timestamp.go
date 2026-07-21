package webdav

import (
	"context"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

type webDAVMetadataObj struct {
	model.Obj
	metadata model.WebDAVMetadata
}

func (o *webDAVMetadataObj) ModTime() time.Time {
	if o.metadata.HasModTime {
		return time.Unix(o.metadata.ModTime, 0)
	}
	return o.Obj.ModTime()
}

func (o *webDAVMetadataObj) CreateTime() time.Time {
	if o.metadata.HasCreateTime {
		return time.Unix(o.metadata.CreateTime, 0)
	}
	return o.Obj.CreateTime()
}

func newWebDAVMetadata(name string, obj model.Obj) model.WebDAVMetadata {
	hashType, hash := webDAVObjHash(obj)
	return model.WebDAVMetadata{
		Path:     slashClean(name),
		Size:     obj.GetSize(),
		IsDir:    obj.IsDir(),
		ObjectID: obj.GetID(),
		HashType: hashType,
		Hash:     hash,
	}
}

func webDAVObjHash(obj model.Obj) (string, string) {
	hashes := obj.GetHash()
	for _, hashType := range []*utils.HashType{utils.SHA256, utils.SHA1, utils.MD5} {
		if value := hashes.GetHash(hashType); value != "" {
			return hashType.Name, strings.ToLower(value)
		}
	}
	return "", ""
}

func webDAVMetadataMatchesObj(metadata *model.WebDAVMetadata, obj model.Obj) bool {
	if metadata.Size >= 0 && metadata.Size != obj.GetSize() {
		return false
	}
	if metadata.IsDir != obj.IsDir() {
		return false
	}
	if metadata.ObjectID != "" {
		if obj.GetID() == "" || metadata.ObjectID != obj.GetID() {
			return false
		}
	}
	if metadata.HashType != "" && metadata.Hash != "" {
		if hashType, ok := utils.GetHashByName(metadata.HashType); ok {
			if value := obj.GetHash().GetHash(hashType); value != "" && !strings.EqualFold(metadata.Hash, value) {
				return false
			}
		}
	}
	return true
}

func applyWebDAVMetadata(ctx context.Context, name string, obj model.Obj, metadata *model.WebDAVMetadata) model.Obj {
	if metadata == nil || metadata.Path != slashClean(name) || !webDAVMetadataMatchesObj(metadata, obj) {
		return obj
	}
	changed := false
	if metadata.ObjectID == "" && obj.GetID() != "" {
		metadata.ObjectID = obj.GetID()
		changed = true
	}
	if metadata.Hash == "" {
		if hashType, hash := webDAVObjHash(obj); hash != "" {
			metadata.HashType = hashType
			metadata.Hash = hash
			changed = true
		}
	}
	if changed {
		if err := db.UpsertWebDAVMetadata(ctx, metadata); err != nil {
			log.Warnf("failed to bind WebDAV metadata identity for %s: %+v", name, err)
		}
	}
	return &webDAVMetadataObj{Obj: obj, metadata: *metadata}
}

func loadWebDAVMetadata(ctx context.Context, name string, depth int) (map[string]model.WebDAVMetadata, error) {
	if depth == 0 {
		metadata, found, err := db.GetWebDAVMetadata(ctx, name)
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]model.WebDAVMetadata{}, nil
		}
		return map[string]model.WebDAVMetadata{metadata.Path: *metadata}, nil
	}
	if depth == 1 {
		return db.GetWebDAVMetadataForDirectory(ctx, name)
	}
	return db.GetWebDAVMetadataTree(ctx, name)
}
