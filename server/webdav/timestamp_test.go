package webdav

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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
