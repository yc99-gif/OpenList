package webdav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"sort"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const (
	maxWebDAVDeadPropertySize   = 64 << 10
	maxWebDAVDeadPropertiesSize = 1 << 20
)

type storedWebDAVDeadProperty struct {
	Space    string `json:"space"`
	Local    string `json:"local"`
	Lang     string `json:"lang,omitempty"`
	InnerXML []byte `json:"inner_xml,omitempty"`
}

func decodeWebDAVDeadProperties(value string) (map[xml.Name]Property, error) {
	result := map[xml.Name]Property{}
	if value == "" {
		return result, nil
	}
	var stored []storedWebDAVDeadProperty
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return nil, err
	}
	for _, property := range stored {
		name := xml.Name{Space: property.Space, Local: property.Local}
		result[name] = Property{
			XMLName:  name,
			Lang:     property.Lang,
			InnerXML: append([]byte(nil), property.InnerXML...),
		}
	}
	return result, nil
}

func encodeWebDAVDeadProperties(properties map[xml.Name]Property) (string, error) {
	if len(properties) == 0 {
		return "", nil
	}
	stored := make([]storedWebDAVDeadProperty, 0, len(properties))
	for name, property := range properties {
		stored = append(stored, storedWebDAVDeadProperty{
			Space:    name.Space,
			Local:    name.Local,
			Lang:     property.Lang,
			InnerXML: append([]byte(nil), property.InnerXML...),
		})
	}
	sort.Slice(stored, func(i, j int) bool {
		if stored[i].Space != stored[j].Space {
			return stored[i].Space < stored[j].Space
		}
		return stored[i].Local < stored[j].Local
	})
	encoded, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func webDAVMetadataHasState(metadata *model.WebDAVMetadata) bool {
	return metadata.HasModTime || metadata.HasCreateTime ||
		metadata.ContentHash != "" || metadata.DeadProperties != ""
}

func patchWebDAVProperties(ctx context.Context, name string, obj model.Obj, patches []Proppatch) ([]Propstat, error) {
	allProperties := make([]Property, 0)
	protected := make([]Property, 0)
	for _, patch := range patches {
		for _, property := range patch.Props {
			propertyName := Property{XMLName: property.XMLName}
			allProperties = append(allProperties, propertyName)
			if property.XMLName != ownCloudLastModifiedProperty && property.XMLName != ownCloudChecksumsProperty {
				if _, ok := liveProps[property.XMLName]; ok {
					protected = append(protected, propertyName)
				}
			}
		}
	}
	if len(protected) != 0 {
		protectedNames := make(map[xml.Name]struct{}, len(protected))
		for _, property := range protected {
			protectedNames[property.XMLName] = struct{}{}
		}
		failed := make([]Property, 0, len(allProperties)-len(protected))
		for _, property := range allProperties {
			if _, ok := protectedNames[property.XMLName]; !ok {
				failed = append(failed, property)
			}
		}
		return makePropstats(
			Propstat{
				Props:    protected,
				Status:   http.StatusForbidden,
				XMLError: `<D:cannot-modify-protected-property xmlns:D="DAV:"/>`,
			},
			Propstat{Props: failed, Status: StatusFailedDependency},
		), nil
	}

	metadata := newWebDAVMetadata(name, obj)
	existing, found, err := db.GetWebDAVMetadata(ctx, name)
	if err != nil {
		return nil, err
	}
	if found && webDAVMetadataMatchesObj(existing, obj) {
		metadata = *existing
		metadata.Path = slashClean(name)
		metadata.Size = obj.GetSize()
		metadata.IsDir = obj.IsDir()
	}

	deadProperties, err := decodeWebDAVDeadProperties(metadata.DeadProperties)
	if err != nil {
		return nil, err
	}
	for _, patch := range patches {
		for _, property := range patch.Props {
			if property.XMLName == ownCloudChecksumsProperty {
				// ownCloud/rclone can set checksums together with lastmodified.
				// OpenList exposes its independently computed plaintext checksum,
				// so the client-supplied value is acknowledged but not trusted.
				continue
			}
			if property.XMLName == ownCloudLastModifiedProperty {
				if patch.Remove {
					metadata.ModTime = 0
					metadata.ModTimeNsec = 0
					metadata.HasModTime = false
					continue
				}
				value, ok := parseWebDAVPropertyTime(string(property.InnerXML))
				if !ok {
					return failedWebDAVPatch(allProperties, property.XMLName, http.StatusConflict), nil
				}
				setWebDAVModTime(&metadata, value)
				continue
			}

			if patch.Remove {
				delete(deadProperties, property.XMLName)
				continue
			}
			if len(property.InnerXML) > maxWebDAVDeadPropertySize {
				return failedWebDAVPatch(allProperties, property.XMLName, StatusInsufficientStorage), nil
			}
			deadProperties[property.XMLName] = Property{
				XMLName:  property.XMLName,
				Lang:     property.Lang,
				InnerXML: append([]byte(nil), property.InnerXML...),
			}
		}
	}

	metadata.DeadProperties, err = encodeWebDAVDeadProperties(deadProperties)
	if err != nil {
		return nil, err
	}
	if len(metadata.DeadProperties) > maxWebDAVDeadPropertiesSize {
		return []Propstat{{Props: allProperties, Status: StatusInsufficientStorage}}, nil
	}
	if webDAVMetadataHasState(&metadata) {
		err = db.UpsertWebDAVMetadata(ctx, &metadata)
	} else {
		err = db.DeleteWebDAVMetadata(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	return []Propstat{{Props: allProperties, Status: http.StatusOK}}, nil
}

func failedWebDAVPatch(all []Property, failedName xml.Name, status int) []Propstat {
	failed := make([]Property, 0, 1)
	dependencies := make([]Property, 0, len(all)-1)
	foundFailed := false
	for _, property := range all {
		if !foundFailed && property.XMLName == failedName {
			failed = append(failed, property)
			foundFailed = true
			continue
		}
		dependencies = append(dependencies, property)
	}
	return makePropstats(
		Propstat{Props: failed, Status: status},
		Propstat{Props: dependencies, Status: StatusFailedDependency},
	)
}
