package webdav

import (
	"context"
	"encoding/xml"
	"net/http"
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
	request.Header.Set("X-OC-Mtime", "1462518489.123456789")
	times = handler.getRequestTimes(request)
	if !times.hasModTime || times.modTime.Nanosecond() != 123456789 {
		t.Fatalf("fractional X-OC-Mtime was not preserved: %+v", times)
	}

	request = httptest.NewRequest("PUT", "/file.txt", nil)
	request.Header.Set("X-OC-Mtime", "invalid")
	times = handler.getRequestTimes(request)
	if times.hasModTime || times.hasCreateTime {
		t.Fatalf("invalid timestamp header was preserved: %+v", times)
	}
}

func TestWebDAVMetadataContentHashAndNanosecondTime(t *testing.T) {
	backendTime := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	preservedTime := time.Date(2016, 5, 6, 7, 8, 9, 123456789, time.UTC)
	obj := &model.Object{Name: "file.txt", Size: 12, Modified: backendTime}
	metadata := newWebDAVMetadata("/file.txt", obj)
	setWebDAVModTime(&metadata, preservedTime)
	metadata.ContentHashType = utils.SHA256.Name
	metadata.ContentHash = "0123456789abcdef"

	wrapped := applyWebDAVMetadata(context.Background(), "/file.txt", obj, &metadata)
	if !wrapped.ModTime().Equal(preservedTime) {
		t.Fatalf("wrapped modification time = %s, want %s", wrapped.ModTime(), preservedTime)
	}
	if got := wrapped.GetHash().GetHash(utils.SHA256); got != metadata.ContentHash {
		t.Fatalf("wrapped SHA-256 = %q, want %q", got, metadata.ContentHash)
	}
	etag, err := findETag(context.Background(), nil, "/file.txt", wrapped)
	if err != nil || etag != `"0123456789abcdef"` {
		t.Fatalf("content ETag = %q, err=%v", etag, err)
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

	differentBackendTime := *obj
	differentBackendTime.Modified = obj.Modified.Add(time.Second)
	if webDAVMetadataMatchesObj(&metadata, &differentBackendTime) {
		t.Fatal("backing modification time mismatch was accepted")
	}

	wrapped := applyWebDAVMetadata(nil, "/file.txt", obj, &metadata)
	if wrapped.ModTime().Unix() != metadata.ModTime {
		t.Fatalf("wrapped modification time = %d, want %d", wrapped.ModTime().Unix(), metadata.ModTime)
	}
}

func TestBackendModTimeBindingWaitsForProviderStability(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	obj := &model.Object{Modified: now.Add(-time.Minute)}
	metadata := &model.WebDAVMetadata{UpdatedAt: now.Add(-webDAVBackendIdentityStabilizationDelay + time.Second)}
	if shouldBindWebDAVBackendModTime(metadata, obj, now) {
		t.Fatal("backend modification time was bound before the stabilization delay")
	}
	metadata.UpdatedAt = now.Add(-webDAVBackendIdentityStabilizationDelay)
	if !shouldBindWebDAVBackendModTime(metadata, obj, now) {
		t.Fatal("stable backend modification time was not eligible for binding")
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

	propstats, err := patchWebDAVProperties(ctx, "/file.txt", obj, setPatch)
	if err != nil {
		t.Fatalf("set lastmodified: %v", err)
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
	propstats, err = patchWebDAVProperties(ctx, "/file.txt", obj, invalidPatch)
	if err != nil || len(propstats) != 1 || propstats[0].Status != 409 {
		t.Fatalf("invalid lastmodified response: propstats=%+v err=%v", propstats, err)
	}
	metadata, found, err = db.GetWebDAVMetadata(ctx, "/file.txt")
	if err != nil || !found || metadata.ModTime != 1462518489 {
		t.Fatalf("invalid patch changed metadata: metadata=%+v found=%v err=%v", metadata, found, err)
	}

	removePatch := []Proppatch{{Remove: true, Props: []Property{{XMLName: ownCloudLastModifiedProperty}}}}
	propstats, err = patchWebDAVProperties(ctx, "/file.txt", obj, removePatch)
	if err != nil || len(propstats) != 1 || propstats[0].Status != 200 {
		t.Fatalf("remove lastmodified: propstats=%+v err=%v", propstats, err)
	}
	if _, found, err = db.GetWebDAVMetadata(ctx, "/file.txt"); err != nil || found {
		t.Fatalf("removed lastmodified remains: found=%v err=%v", found, err)
	}

	unsupported := []Proppatch{{Props: []Property{{
		XMLName:  xml.Name{Space: "DAV:", Local: "getlastmodified"},
		InnerXML: []byte("1462518489"),
	}}}}
	propstats, err = patchWebDAVProperties(ctx, "/file.txt", obj, unsupported)
	if err != nil || len(propstats) != 1 || propstats[0].Status != http.StatusForbidden {
		t.Fatalf("protected property response: propstats=%+v err=%v", propstats, err)
	}
}

func TestParseWebDAVPropertyTime(t *testing.T) {
	testCases := []struct {
		value      string
		unix       int64
		nanosecond int
	}{
		{"1462518489", 1462518489, 0},
		{"1462518489.123456789", 1462518489, 123456789},
		{"Fri, 06 May 2016 07:08:09 GMT", 1462518489, 0},
		{"2016-05-06T07:08:09Z", 1462518489, 0},
	}
	for _, testCase := range testCases {
		parsed, ok := parseWebDAVPropertyTime(testCase.value)
		if !ok || parsed.Unix() != testCase.unix || parsed.Nanosecond() != testCase.nanosecond {
			t.Errorf("parse %q = %v, %v; want Unix %d with %d nanoseconds", testCase.value, parsed, ok, testCase.unix, testCase.nanosecond)
		}
	}
	if _, ok := parseWebDAVPropertyTime("invalid"); ok {
		t.Fatal("invalid property time was accepted")
	}
}

func TestPatchWebDAVDeadPropertiesAtomically(t *testing.T) {
	conf.Conf = conf.DefaultConfig("data")
	testDB, err := gorm.Open(sqlite.Open("file:webdav_dead_properties?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db.Init(testDB)
	ctx := context.Background()
	obj := &model.Object{
		ID:       "object-id",
		Name:     "file.txt",
		Size:     12,
		Modified: time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC),
	}
	customName := xml.Name{Space: "urn:openlist:test", Local: "revision"}
	setPatches := []Proppatch{{Props: []Property{
		{XMLName: customName, Lang: "zh-CN", InnerXML: []byte(`<value xmlns="urn:openlist:test">17</value>`)},
		{XMLName: ownCloudLastModifiedProperty, InnerXML: []byte("2016-05-06T07:08:09.123456789Z")},
		{XMLName: ownCloudChecksumsProperty, InnerXML: []byte(`<checksum xmlns="http://owncloud.org/ns">SHA1:client-value</checksum>`)},
	}}}

	propstats, err := patchWebDAVProperties(ctx, "/file.txt", obj, setPatches)
	if err != nil || len(propstats) != 1 || propstats[0].Status != 200 {
		t.Fatalf("set mixed properties: propstats=%+v err=%v", propstats, err)
	}
	metadata, found, err := db.GetWebDAVMetadata(ctx, "/file.txt")
	if err != nil || !found {
		t.Fatalf("load mixed properties: found=%v err=%v", found, err)
	}
	if metadata.ModTimeNsec != time.Date(2016, 5, 6, 7, 8, 9, 123456789, time.UTC).UnixNano() {
		t.Fatalf("nanosecond modification time was not stored: %+v", metadata)
	}

	wrapped := applyWebDAVMetadata(ctx, "/file.txt", obj, metadata)
	holder, ok := wrapped.(DeadPropsReader)
	if !ok {
		t.Fatalf("metadata wrapper was rejected: wrapped=%T metadata=%+v obj=%+v", wrapped, metadata, obj)
	}
	deadProperties, err := holder.DeadProps()
	if err != nil {
		t.Fatalf("decode dead properties: %v", err)
	}
	if _, ok := deadProperties[customName]; !ok {
		t.Fatalf("stored dead property is missing: properties=%+v metadata=%+v", deadProperties, metadata)
	}
	pstats, err := props(ctx, nil, wrapped, []xml.Name{customName})
	if err != nil || len(pstats) != 1 || pstats[0].Status != 200 || len(pstats[0].Props) != 1 {
		t.Fatalf("read dead property: propstats=%+v err=%v", pstats, err)
	}
	if got := string(pstats[0].Props[0].InnerXML); got != `<value xmlns="urn:openlist:test">17</value>` {
		t.Fatalf("dead property value = %q", got)
	}

	protectedPatch := []Proppatch{{Props: []Property{
		{XMLName: customName, InnerXML: []byte("18")},
		{XMLName: xml.Name{Space: "DAV:", Local: "getetag"}, InnerXML: []byte("forbidden")},
	}}}
	propstats, err = patchWebDAVProperties(ctx, "/file.txt", obj, protectedPatch)
	if err != nil || len(propstats) != 2 {
		t.Fatalf("protected mixed patch: propstats=%+v err=%v", propstats, err)
	}
	metadataAfter, found, err := db.GetWebDAVMetadata(ctx, "/file.txt")
	if err != nil || !found || metadataAfter.DeadProperties != metadata.DeadProperties {
		t.Fatalf("protected patch was not atomic: before=%q after=%q found=%v err=%v", metadata.DeadProperties, metadataAfter.DeadProperties, found, err)
	}
}
