package webdav

import (
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
	"strings"
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
		if parsed, ok := parseWebDAVHeaderTime(hVal); ok {
			return parsed, true
		}
		log.Warnf("failed to parse WebDAV %s header: invalid timestamp %q", header, hVal)
	}
	return time.Time{}, false
}

func parseWebDAVHeaderTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if unixTime, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unixTime, 0), true
	}
	parts := strings.SplitN(value, ".", 2)
	if len(parts) == 2 && len(parts[1]) > 0 && len(parts[1]) <= 9 {
		seconds, err := strconv.ParseInt(parts[0], 10, 64)
		if err == nil {
			fraction := parts[1] + strings.Repeat("0", 9-len(parts[1]))
			nanoseconds, fractionErr := strconv.ParseInt(fraction, 10, 32)
			if fractionErr == nil {
				if strings.HasPrefix(parts[0], "-") {
					nanoseconds = -nanoseconds
				}
				return time.Unix(seconds, nanoseconds), true
			}
		}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}
