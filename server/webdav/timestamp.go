package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	log "github.com/sirupsen/logrus"
)

var ownCloudLastModifiedProperty = xml.Name{Space: "DAV:", Local: "lastmodified"}

type webDAVMetadataObj struct {
	model.Obj
	metadata model.WebDAVMetadata
}

const webDAVBackendIdentityStabilizationDelay = 5 * time.Minute

func wrapWebDAVObj(obj model.Obj) model.Obj {
	if _, ok := obj.(*webDAVMetadataObj); ok {
		return obj
	}
	return &webDAVMetadataObj{Obj: obj}
}

func (o *webDAVMetadataObj) ModTime() time.Time {
	if o.metadata.HasModTime {
		return webDAVStoredTime(o.metadata.ModTime, o.metadata.ModTimeNsec)
	}
	return o.Obj.ModTime()
}

func (o *webDAVMetadataObj) CreateTime() time.Time {
	if o.metadata.HasCreateTime {
		return webDAVStoredTime(o.metadata.CreateTime, o.metadata.CreateTimeNsec)
	}
	return o.Obj.CreateTime()
}

func (o *webDAVMetadataObj) GetHash() utils.HashInfo {
	if o.metadata.ContentHashType != "" && o.metadata.ContentHash != "" {
		if hashType, ok := utils.GetHashByName(o.metadata.ContentHashType); ok {
			return utils.NewHashInfo(hashType, o.metadata.ContentHash)
		}
	}
	return o.Obj.GetHash()
}

// GetETag is consumed by server/common so transparent proxy responses and
// WebDAV PROPFIND use the same entity tag.
func (o *webDAVMetadataObj) GetETag() string {
	if o.metadata.ContentHash != "" {
		return fmt.Sprintf(`"%s"`, strings.ToLower(o.metadata.ContentHash))
	}
	return common.GetEtag(o.Obj, o.GetSize())
}

func (o *webDAVMetadataObj) ETag(context.Context) (string, error) {
	return o.GetETag(), nil
}

func (o *webDAVMetadataObj) DeadProps() (map[xml.Name]Property, error) {
	return decodeWebDAVDeadProperties(o.metadata.DeadProperties)
}

func webDAVStoredTime(seconds, nanoseconds int64) time.Time {
	if nanoseconds != 0 {
		return time.Unix(0, nanoseconds)
	}
	return time.Unix(seconds, 0)
}

func setWebDAVModTime(metadata *model.WebDAVMetadata, value time.Time) {
	metadata.ModTime = value.Unix()
	metadata.ModTimeNsec = value.UnixNano()
	metadata.HasModTime = true
}

func setWebDAVCreateTime(metadata *model.WebDAVMetadata, value time.Time) {
	metadata.CreateTime = value.Unix()
	metadata.CreateTimeNsec = value.UnixNano()
	metadata.HasCreateTime = true
}

func newWebDAVMetadata(name string, obj model.Obj) model.WebDAVMetadata {
	hashType, hash := webDAVObjHash(obj)
	backendModTimeNsec := int64(0)
	if !obj.ModTime().IsZero() {
		backendModTimeNsec = obj.ModTime().UnixNano()
	}
	return model.WebDAVMetadata{
		Path:               slashClean(name),
		Size:               obj.GetSize(),
		IsDir:              obj.IsDir(),
		ObjectID:           obj.GetID(),
		HashType:           hashType,
		Hash:               hash,
		BackendModTimeNsec: backendModTimeNsec,
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
	if metadata.BackendModTimeNsec != 0 && !obj.ModTime().IsZero() && metadata.BackendModTimeNsec != obj.ModTime().UnixNano() {
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
	if metadata.BackendModTimeNsec == 0 && shouldBindWebDAVBackendModTime(metadata, obj, time.Now()) {
		metadata.BackendModTimeNsec = obj.ModTime().UnixNano()
		changed = true
	}
	if changed {
		if err := db.UpsertWebDAVMetadata(ctx, metadata); err != nil {
			log.Warnf("failed to bind WebDAV metadata identity for %s: %+v", name, err)
		}
	}
	return &webDAVMetadataObj{Obj: obj, metadata: *metadata}
}

func shouldBindWebDAVBackendModTime(metadata *model.WebDAVMetadata, obj model.Obj, now time.Time) bool {
	if obj.ModTime().IsZero() || metadata.UpdatedAt.IsZero() {
		return false
	}
	return !now.Before(metadata.UpdatedAt.Add(webDAVBackendIdentityStabilizationDelay))
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

func parseWebDAVPropertyTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if parsed, ok := parseWebDAVHeaderTime(value); ok {
		return parsed, true
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}
