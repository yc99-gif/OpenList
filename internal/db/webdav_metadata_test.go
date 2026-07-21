package db

import (
	"context"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupWebDAVMetadataTestDB(t *testing.T) {
	t.Helper()
	oldDB := db
	testDB, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db = testDB
	if err = testDB.AutoMigrate(&model.WebDAVMetadata{}); err != nil {
		t.Fatalf("migrate WebDAV metadata: %v", err)
	}
	t.Cleanup(func() {
		db = oldDB
	})
}

func TestWebDAVMetadataLifecycle(t *testing.T) {
	setupWebDAVMetadataTestDB(t)
	ctx := context.Background()
	modTime := time.Date(2016, 5, 6, 7, 8, 9, 0, time.UTC).Unix()

	source := []model.WebDAVMetadata{
		{Path: "/source", IsDir: true, HasModTime: true, ModTime: modTime},
		{Path: "/source/file.txt", Size: 12, HasModTime: true, ModTime: modTime, ObjectID: "source-id", HashType: "md5", Hash: "abc"},
		{Path: "/source/sub/file.txt", Size: 8, HasModTime: true, ModTime: modTime},
		{Path: "/source%/literal.txt", Size: 1, HasModTime: true, ModTime: modTime},
	}
	for i := range source {
		if err := UpsertWebDAVMetadata(ctx, &source[i]); err != nil {
			t.Fatalf("upsert %s: %v", source[i].Path, err)
		}
	}

	metadata, found, err := GetWebDAVMetadata(ctx, "/source/file.txt")
	if err != nil || !found {
		t.Fatalf("get metadata: found=%v err=%v", found, err)
	}
	if metadata.ModTime != modTime || metadata.Size != 12 || metadata.ObjectID != "source-id" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}

	directory, err := GetWebDAVMetadataForDirectory(ctx, "/source")
	if err != nil {
		t.Fatalf("get directory metadata: %v", err)
	}
	if len(directory) != 2 {
		t.Fatalf("direct directory metadata count = %d, want 2", len(directory))
	}
	if _, ok := directory["/source/sub/file.txt"]; ok {
		t.Fatal("Depth: 1 metadata unexpectedly contains a grandchild")
	}

	if err = UpsertWebDAVMetadata(ctx, &model.WebDAVMetadata{Path: "/destination/old.txt", HasModTime: true, ModTime: 1}); err != nil {
		t.Fatalf("upsert destination metadata: %v", err)
	}
	if err = CopyWebDAVMetadataTree(ctx, "/source", "/destination"); err != nil {
		t.Fatalf("copy metadata: %v", err)
	}
	if _, found, err = GetWebDAVMetadata(ctx, "/destination/old.txt"); err != nil || found {
		t.Fatalf("overwritten destination metadata remains: found=%v err=%v", found, err)
	}
	copied, found, err := GetWebDAVMetadata(ctx, "/destination/file.txt")
	if err != nil || !found {
		t.Fatalf("get copied metadata: found=%v err=%v", found, err)
	}
	if copied.ObjectID != "" || copied.Hash != "abc" {
		t.Fatalf("copied identity not rewritten correctly: %+v", copied)
	}
	if _, found, err = GetWebDAVMetadata(ctx, "/source/file.txt"); err != nil || !found {
		t.Fatalf("copy removed source metadata: found=%v err=%v", found, err)
	}

	if err = MoveWebDAVMetadataTree(ctx, "/destination", "/moved"); err != nil {
		t.Fatalf("move metadata: %v", err)
	}
	if _, found, err = GetWebDAVMetadata(ctx, "/destination/file.txt"); err != nil || found {
		t.Fatalf("move left source metadata: found=%v err=%v", found, err)
	}
	if _, found, err = GetWebDAVMetadata(ctx, "/moved/sub/file.txt"); err != nil || !found {
		t.Fatalf("move lost descendant metadata: found=%v err=%v", found, err)
	}

	if err = DeleteWebDAVMetadataTree(ctx, "/moved"); err != nil {
		t.Fatalf("delete metadata tree: %v", err)
	}
	if tree, err := GetWebDAVMetadataTree(ctx, "/moved"); err != nil || len(tree) != 0 {
		t.Fatalf("metadata tree remains after delete: count=%d err=%v", len(tree), err)
	}
	if _, found, err = GetWebDAVMetadata(ctx, "/source%/literal.txt"); err != nil || !found {
		t.Fatalf("literal percent path was overmatched: found=%v err=%v", found, err)
	}
}
