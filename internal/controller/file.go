package controller

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/pkg/global"
	"io"
	"net/http"
)

// UploadFile -
func UploadFile(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, global.ErrorFileNotFound)
		return
	}
	if file.Size > maxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, global.ErrorFileUploadError)
		return
	}
	if !uploadContentTypeAllowed(file.Header.Get("Content-Type")) {
		c.JSON(http.StatusBadRequest, global.ErrorParseFile)
		return
	}
	if manager.GetManager().GetBucket() == nil {
		c.JSON(http.StatusServiceUnavailable, global.ErrorFileUploadError)
		return
	}
	parentDir := safeObjectParentDir(c.DefaultQuery("parentDir", "word"))

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, global.ErrorParseFile)
		return
	}
	defer src.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, src); err != nil {
		c.JSON(http.StatusBadRequest, global.ErrorFileHashError)
	}
	objectKey := fmt.Sprintf("%s/%s/%s", parentDir, hex.EncodeToString(hash.Sum(nil)), safeUploadFilename(file.Filename))
	// 重新打开文件以保存
	src.Seek(0, io.SeekStart)
	err = manager.GetManager().GetBucket().PutObject(objectKey, src)
	if err != nil {
		c.JSON(http.StatusBadRequest, global.ErrorFileUploadError)
		return
	}
	c.String(http.StatusOK, objectKey)
}
