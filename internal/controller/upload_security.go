package controller

import (
	"mime"
	"path/filepath"
	"strings"
)

const maxUploadBytes = int64(16 << 20)

var allowedUploadContentTypes = map[string]struct{}{
	"application/json":         {},
	"application/msword":       {},
	"application/pdf":          {},
	"application/vnd.ms-excel": {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
	"text/plain": {},
}

func normalizeUploadContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func uploadContentTypeAllowed(value string) bool {
	_, ok := allowedUploadContentTypes[normalizeUploadContentType(value)]
	return ok
}

func safeObjectParentDir(value string) string {
	normalized := strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	if normalized == "" || len(normalized) > 128 {
		return "word"
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == "" || part == "." || part == ".." {
			return "word"
		}
	}
	return normalized
}

func safeUploadFilename(value string) string {
	normalized := filepath.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if normalized == "" || normalized == "." || normalized == ".." {
		return "upload"
	}
	return normalized
}
