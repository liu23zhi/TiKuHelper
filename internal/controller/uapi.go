package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

// maxLocationLookupKeys 是单次批量归属地查询允许的最大 key 数量。
//
// 用户管理 / 日志列表一页最多 100 行，取 100 与之对齐；
// 再大就容易被上游速率限制拒绝，也会让一次请求耗时过长。
const maxLocationLookupKeys = 100

// UAPIPhoneInfo 查询手机号归属地。
//
// 同时支持单值与批量：
//
//	GET /adapter-service/uapi/phoneinfo?phone=13800138000
//	GET /adapter-service/uapi/phoneinfo?phones=13800138000,13900139000
//
// 响应用对象按 key 索引，便于前端 O(1) 取用：
//
//	{"data":{"13800138000":{"address":"云南玉溪 移动","province":"云南",...}}}
func UAPIPhoneInfo(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	phones, errMessage := collectLocationLookupKeys(c, "phone", "phones")
	if errMessage != "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": errMessage})
		return
	}

	// 用与搜题链路相同的安全取库方式：manager 未初始化时返回 nil，避免空指针崩溃。
	db := resolveSearchPersistenceDB()
	locations := service.LookupPhoneLocationsWithDB(db, phones, service.LocationQueryOptions{
		UAPIKey: service.GetUAPIAPIKeyWithDB(db),
	})

	c.JSON(http.StatusOK, gin.H{"data": locations})
}

// UAPIIPInfo 查询 IP 归属地。
//
//	GET /adapter-service/uapi/ipinfo?ip=8.8.8.8
//	GET /adapter-service/uapi/ipinfo?ips=8.8.8.8,1.1.1.1&source=commercial
func UAPIIPInfo(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	ips, errMessage := collectLocationLookupKeys(c, "ip", "ips")
	if errMessage != "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": errMessage})
		return
	}

	source := strings.TrimSpace(c.Query("source"))
	if source != "" && source != uapi.SourceCommercial {
		// 文档里 source 只有 commercial 这一个合法取值。
		c.JSON(http.StatusBadRequest, gin.H{"message": "source 只支持 commercial"})
		return
	}

	// 用与搜题链路相同的安全取库方式：manager 未初始化时返回 nil，避免空指针崩溃。
	db := resolveSearchPersistenceDB()
	locations := service.LookupIPLocationsWithDB(db, ips, service.LocationQueryOptions{
		UAPIKey: service.GetUAPIAPIKeyWithDB(db),
		Source:  source,
	})

	c.JSON(http.StatusOK, gin.H{"data": locations})
}

// collectLocationLookupKeys 解析单值与批量两种入参形式。
//
// 单值参数名与批量参数名都接受，便于调用方按需选择；
// 两者都为空时返回明确错误，而不是静默返回空结果。
func collectLocationLookupKeys(c *gin.Context, singleKey string, bulkKey string) ([]string, string) {
	raw := make([]string, 0, maxLocationLookupKeys)

	if single := strings.TrimSpace(c.Query(singleKey)); single != "" {
		raw = append(raw, single)
	}
	if bulk := strings.TrimSpace(c.Query(bulkKey)); bulk != "" {
		for _, item := range strings.Split(bulk, ",") {
			raw = append(raw, item)
		}
	}

	if len(raw) == 0 {
		return nil, "缺少参数：" + singleKey + " 或 " + bulkKey
	}
	if len(raw) > maxLocationLookupKeys {
		return nil, "单次查询最多 " + strconv.Itoa(maxLocationLookupKeys) + " 个"
	}
	return raw, ""
}
