package webdav

import (
	"context"
	"encoding/xml"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGetRequestTimes(t *testing.T) {
	handler := &Handler{}

	request := httptest.NewRequest("PUT", "/file.txt", nil)
	before := time.Now()
	times := handler.getRequestTimes(request)
	after := time.Now()
	if times.hasModTime || times.hasCreateTime {
		t.Fatal("request without timestamp headers was marked explicit")
	}
	if times.modTime.Before(before) || times.modTime.After(after) || times.createTime.Before(before) || times.createTime.After(after) {
		t.Fatalf("default request times are outside the request window: %+v", times)
	}

	request = httptest.NewRequest("PUT", "/file.txt", nil)
	request.Header.Set("X-OC-Mtime", "1462518489")
	times = handler.getRequestTimes(request)
	if !times.hasModTime || !times.hasCreateTime {
		t.Fatalf("X-OC-Mtime did not mark both preserved times: %+v", times)
	}
	if times.modTime.Unix() != 1462518489 || times.createTime.Unix() != 1462518489 {
		t.Fatalf("X-OC-Mtime was not parsed correctly: %+v", times)
	}

	request.Header.Set("X-OC-Ctime", "1462518000")
	times = handler.getRequestTimes(request)
	if times.createTime.Unix() != 1462518000 {
		t.Fatalf("X-OC-Ctime was not preferred: %+v", times)
	}

	request = httptest.NewRequest("PUT", "/file.txt", nil)
	request.Header.Set("X-OC-Mtime", "invalid")
	times = handler.getRequestTimes(request)
	if times.hasModTime || times.hasCreateTime {
		t.Fatalf("invalid timestamp header was preserved: %+v", times)
	}
}

func TestWebDAVMetadataMatchesObj(t *testing.T) {
	obj := &model.Object{
		ID:       "object-id",
		Name:     "file.txt",
		Size:     12,
		Modified: time.Now(),
		HashInfo: utils.NewHashInfo(utils.MD5, "abcdef"),
	}
	metadata := newWebDAVMetadata("/file.txt", obj)
	metadata.HasModTime = true
	metadata.ModTime = 1462518489
	if !webDAVMetadataMatchesObj(&metadata, obj) {
		t.Fatal("matching metadata was rejected")
	}

	differentSize := *obj
	differentSize.Size++
	if webDAVMetadataMatchesObj(&metadata, &differentSize) {
		t.Fatal("size mismatch was accepted")
	}

	differentID := *obj
	differentID.ID = "other-id"
	if webDAVMetadataMatchesObj(&metadata, &differentID) {
		t.Fatal("object ID mismatch was accepted")
	}

	differentHash := *obj
	differentHash.HashInfo = utils.NewHashInfo(utils.MD5, "fedcba")
	if webDAVMetadataMatchesObj(&metadata, &differentHash) {
		t.Fatal("hash mismatch was accepted")
	}

	wrapped := applyWebDAVMetadata(nil, "/file.txt", obj, &metadata)
	if wrapped.ModTime().Unix() != metadata.ModTime {
		t.Fatalf("wrapped modification time = %d, want %d", wrapped.ModTime().Unix(), metadata.ModTime)
	}
}

func TestPatchWebDAVTimestamp(t *testing.T) {
	conf.Conf = conf.DefaultConfig("data")
	testDB, err := gorm.Open(sqlite.Open("file:webdav_timestamp_patch?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db.Init(testDB)
	ctx := context.Background()
	obj := &model.Object{ID: "object-id", Name: "file.txt", Size: 12}
	setPatch := []Proppatch{{Props: []Property{{
		XMLName:  ownCloudLastModifiedProperty,
		InnerXML: []byte("1462518489"),
	}}}}

	propstats, handled, err := patchWebDAVTimestamp(ctx, "/file.txt", obj, setPatch)
	if err != nil || !handled {
		t.Fatalf("set lastmodified: handled=%v err=%v", handled, err)
	}
	if len(propstats) != 1 || propstats[0].Status != 200 {
		t.Fatalf("unexpected set propstats: %+v", propstats)
	}
	metadata, found, err := db.GetWebDAVMetadata(ctx, "/file.txt")
	if err != nil || !found || !metadata.HasModTime || metadata.ModTime != 1462518489 {
		t.Fatalf("stored lastmodified: metadata=%+v found=%v err=%v", metadata, found, err)
	}

	invalidPatch := []Proppatch{{Props: []Property{{
		XMLName:  ownCloudLastModifiedProperty,
		InnerXML: []byte("not-a-time"),
	}}}}
	propstats, handled, err = patchWebDAVTimestamp(ctx, "/file.txt", obj, invalidPatch)
	if err != nil || !handled || len(propstats) != 1 || propstats[0].Status != 409 {
		t.Fatalf("invalid lastmodified response: handled=%v propstats=%+v err=%v", handled, propstats, err)
	}
	metadata, found, err = db.GetWebDAVMetadata(ctx, "/file.txt")
	if err != nil || !found || metadata.ModTime != 1462518489 {
		t.Fatalf("invalid patch changed metadata: metadata=%+v found=%v err=%v", metadata, found, err)
	}

	removePatch := []Proppatch{{Remove: true, Props: []Property{{XMLName: ownCloudLastModifiedProperty}}}}
	propstats, handled, err = patchWebDAVTimestamp(ctx, "/file.txt", obj, removePatch)
	if err != nil || !handled || len(propstats) != 1 || propstats[0].Status != 200 {
		t.Fatalf("remove lastmodified: handled=%v propstats=%+v err=%v", handled, propstats, err)
	}
	if _, found, err = db.GetWebDAVMetadata(ctx, "/file.txt"); err != nil || found {
		t.Fatalf("removed lastmodified remains: found=%v err=%v", found, err)
	}

	unsupported := []Proppatch{{Props: []Property{{
		XMLName:  xml.Name{Space: "DAV:", Local: "getlastmodified"},
		InnerXML: []byte("1462518489"),
	}}}}
	if _, handled, err = patchWebDAVTimestamp(ctx, "/file.txt", obj, unsupported); err != nil || handled {
		t.Fatalf("protected property was handled: handled=%v err=%v", handled, err)
	}
}

func TestParseWebDAVPropertyTime(t *testing.T) {
	testCases := []struct {
		value string
		unix  int64
	}{
		{"1462518489", 1462518489},
		{"Fri, 06 May 2016 07:08:09 GMT", 1462518489},
		{"2016-05-06T07:08:09Z", 1462518489},
	}
	for _, testCase := range testCases {
		parsed, ok := parseWebDAVPropertyTime(testCase.value)
		if !ok || parsed.Unix() != testCase.unix {
			t.Errorf("parse %q = %v, %v; want Unix %d", testCase.value, parsed, ok, testCase.unix)
		}
	}
	if _, ok := parseWebDAVPropertyTime("invalid"); ok {
		t.Fatal("invalid property time was accepted")
	}
}
