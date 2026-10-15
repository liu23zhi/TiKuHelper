package controller

import (
	"errors"
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
	apiKey := service.GetUAPIAPIKeyWithDB(db)
	locations, lookupErr := service.LookupPhoneLocationsDetailedWithDB(db, phones, service.LocationQueryOptions{
		UAPIKey: apiKey,
	})

	c.JSON(http.StatusOK, gin.H{"data": locations, "configured": apiKey != "", "message": service.LocationQueryErrorMessage(lookupErr)})
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
	apiKey := service.GetUAPIAPIKeyWithDB(db)
	locations, lookupErr := service.LookupIPLocationsDetailedWithDB(db, ips, service.LocationQueryOptions{
		UAPIKey: apiKey,
		Source:  source,
	})

	c.JSON(http.StatusOK, gin.H{"data": locations, "configured": apiKey != "", "message": service.LocationQueryErrorMessage(lookupErr)})
}

// UAPIMyLocations 查询当前用户自己的手机号和登录 IP 归属地。
// 普通用户账单与个人中心只能走此接口，避免把管理员批量查询接口暴露给普通用户。
func UAPIMyLocations(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db := resolveSearchPersistenceDB()
	phones := make([]string, 0, 1)
	if operator.Phone != nil {
		phones = append(phones, *operator.Phone)
	}
	var requested []string
	if c.Query("ips") != "" || c.Query("ip") != "" {
		var errMessage string
		requested, errMessage = collectLocationLookupKeys(c, "ip", "ips")
		if errMessage != "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": errMessage})
			return
		}
	}
	ips, err := service.ResolveOwnLocationIPsWithDB(db, operator, requested)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrLocationForbidden) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrLocationInvalidKey) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"message": service.LocationQueryErrorMessage(err)})
		return
	}
	apiKey := service.GetUAPIAPIKeyWithDB(db)
	phoneLocations := service.LookupPhoneLocationsWithDB(db, phones, service.LocationQueryOptions{UAPIKey: apiKey})
	ipLocations := service.LookupIPLocationsWithDB(db, ips, service.LocationQueryOptions{UAPIKey: apiKey})
	c.JSON(http.StatusOK, gin.H{"phone": phoneLocations, "ip": ipLocations})
}

// LocationCacheList 管理员查看已经持久化的归属地缓存。
func LocationCacheList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	result, err := service.ListLocationCacheWithDB(resolveSearchPersistenceDB(), service.LocationCacheListQuery{
		Kind: c.Query("kind"), Keyword: c.Query("keyword"), Source: c.Query("source"),
	}, page, pageSize)
	if err != nil {
		if errors.Is(err, service.ErrLocationInvalidKey) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取归属地缓存失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// LocationCacheDelete 管理员删除一条归属地缓存。
func LocationCacheDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	if err := service.DeleteLocationCacheWithDB(resolveSearchPersistenceDB(), c.Query("kind"), c.Query("lookupKey"), c.Query("source")); err != nil {
		if errors.Is(err, service.ErrLocationInvalidKey) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "删除归属地缓存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// LocationCacheRefresh 管理员显式刷新一条归属地，失败不清空已有有效地址。
func LocationCacheRefresh(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		Kind      string `json:"kind"`
		LookupKey string `json:"lookupKey"`
		Source    string `json:"source"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "归属地刷新参数错误"})
		return
	}
	db := resolveSearchPersistenceDB()
	apiKey := service.GetUAPIAPIKeyWithDB(db)
	row, err := service.RefreshLocationCacheWithDB(db, input.Kind, input.LookupKey, service.LocationQueryOptions{UAPIKey: apiKey, Source: input.Source})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, service.ErrLocationInvalidKey) {
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrLocationNotConfigured) || errors.Is(err, service.ErrLocationUnavailable) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"message": service.LocationQueryErrorMessage(err), "configured": apiKey != ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": row, "configured": true})
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
