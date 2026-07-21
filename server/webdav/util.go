package webdav

import (
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
	"time"
)

type requestTimes struct {
	modTime       time.Time
	createTime    time.Time
	hasModTime    bool
	hasCreateTime bool
}

func (h *Handler) getRequestTimes(r *http.Request) requestTimes {
	now := time.Now()
	modTime, hasModTime := h.getHeaderTime(r, "X-OC-Mtime")
	if !hasModTime {
		modTime = now
	}
	createTime, hasCreateTime := h.getHeaderTime(r, "X-OC-Ctime")
	if !hasCreateTime {
		if hasModTime {
			createTime = modTime
			hasCreateTime = true
		} else {
			createTime = now
		}
	}
	return requestTimes{
		modTime:       modTime,
		createTime:    createTime,
		hasModTime:    hasModTime,
		hasCreateTime: hasCreateTime,
	}
}

func (h *Handler) getHeaderTime(r *http.Request, header string) (time.Time, bool) {
	hVal := r.Header.Get(header)
	if hVal != "" {
		modTimeUnix, err := strconv.ParseInt(hVal, 10, 64)
		if err == nil {
			return time.Unix(modTimeUnix, 0), true
		}
		log.Warnf("failed to parse WebDAV %s header: %s", header, err)
	}
	return time.Time{}, false
}
