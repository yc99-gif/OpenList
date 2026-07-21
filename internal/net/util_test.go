package net

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckWritePreconditions(t *testing.T) {
	modTime := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	testCases := []struct {
		name   string
		header string
		value  string
		exists bool
		want   bool
	}{
		{name: "create if absent", header: "If-None-Match", value: "*", exists: false, want: true},
		{name: "reject overwrite if absent required", header: "If-None-Match", value: "*", exists: true, want: false},
		{name: "matching strong etag", header: "If-Match", value: `"current"`, exists: true, want: true},
		{name: "weak etag is not a strong match", header: "If-Match", value: `W/"current"`, exists: true, want: false},
		{name: "nonmatching etag", header: "If-Match", value: `"other"`, exists: true, want: false},
		{name: "if match star needs an existing resource", header: "If-Match", value: "*", exists: false, want: false},
		{name: "if none match uses weak comparison", header: "If-None-Match", value: `W/"current"`, exists: true, want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest("PUT", "/file.txt", nil)
			request.Header.Set(testCase.header, testCase.value)
			if got := CheckWritePreconditions(request, `"current"`, testCase.exists, modTime); got != testCase.want {
				t.Fatalf("CheckWritePreconditions() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCheckReadPreconditions(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/file.txt", nil)
	request.Header.Set("If-None-Match", `"current"`)
	response := httptest.NewRecorder()
	response.Header().Set("Etag", `"current"`)

	if !CheckPreconditions(response, request, time.Now()) {
		t.Fatal("matching If-None-Match did not complete the request")
	}
	if response.Code != http.StatusNotModified {
		t.Fatalf("conditional response status = %d, want %d", response.Code, http.StatusNotModified)
	}
}
