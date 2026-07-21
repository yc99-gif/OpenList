package webdav

import (
	"context"
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

var ownCloudLastModifiedProperty = xml.Name{Space: "DAV:", Local: "lastmodified"}

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

// patchWebDAVTimestamp handles the DAV:lastmodified extension used by rclone
// and ownCloud-compatible clients. DAV:getlastmodified remains protected.
func patchWebDAVTimestamp(ctx context.Context, name string, obj model.Obj, patches []Proppatch) ([]Propstat, bool, error) {
	properties := make([]Property, 0)
	for _, patch := range patches {
		for _, property := range patch.Props {
			if property.XMLName != ownCloudLastModifiedProperty {
				return nil, false, nil
			}
			properties = append(properties, Property{XMLName: property.XMLName})
		}
	}

	metadata := newWebDAVMetadata(name, obj)
	existing, found, err := db.GetWebDAVMetadata(ctx, name)
	if err != nil {
		return nil, true, err
	}
	if found && webDAVMetadataMatchesObj(existing, obj) {
		metadata.ModTime = existing.ModTime
		metadata.CreateTime = existing.CreateTime
		metadata.HasModTime = existing.HasModTime
		metadata.HasCreateTime = existing.HasCreateTime
		metadata.CreatedAt = existing.CreatedAt
	}

	for _, patch := range patches {
		for _, property := range patch.Props {
			if patch.Remove {
				metadata.HasModTime = false
				metadata.ModTime = 0
				continue
			}
			value, ok := parseWebDAVPropertyTime(string(property.InnerXML))
			if !ok {
				return []Propstat{{Props: properties, Status: http.StatusConflict}}, true, nil
			}
			metadata.ModTime = value.Unix()
			metadata.HasModTime = true
		}
	}

	if !metadata.HasModTime && !metadata.HasCreateTime {
		err = db.DeleteWebDAVMetadata(ctx, name)
	} else {
		err = db.UpsertWebDAVMetadata(ctx, &metadata)
	}
	if err != nil {
		return nil, true, err
	}
	return []Propstat{{Props: properties, Status: http.StatusOK}}, true, nil
}

func parseWebDAVPropertyTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if unixTime, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unixTime, 0), true
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}
