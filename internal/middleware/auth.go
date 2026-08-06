package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// TenantKey is the gin context key for tenant (license) ID.
const TenantKey = "tenantId"

// TenantHeaderMiddleware trusts the X-License-Id / X-User-Id headers set by
// the Java NMS VmsIntegrationController after NMS-side authentication.
// VMS is an internal service reachable only via the Docker internal network,
// so it does NOT perform its own JWT validation.
//
// Required headers (set by NMS):
//   - X-License-Id: tenant (license) ID
//   - X-User-Id:    operator user ID
func TenantHeaderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		licenseIDStr := c.GetHeader("X-License-Id")
		userIDStr := c.GetHeader("X-User-Id")
		if licenseIDStr == "" || userIDStr == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    40001,
				"message": "missing X-License-Id or X-User-Id header",
			})
			return
		}

		licenseID, err := strconv.Atoi(licenseIDStr)
		if err != nil || licenseID <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    40001,
				"message": "invalid X-License-Id header",
			})
			return
		}
		userID, err := strconv.Atoi(userIDStr)
		if err != nil || userID <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    40001,
				"message": "invalid X-User-Id header",
			})
			return
		}

		c.Set("userId", userID)
		c.Set(TenantKey, licenseID)
		c.Next()
	}
}

// GetTenantID extracts the tenant (license) ID from gin context.
func GetTenantID(c *gin.Context) int {
	if tid, exists := c.Get(TenantKey); exists {
		if v, ok := tid.(int); ok {
			return v
		}
	}
	return 0
}

// GetUserID extracts the user ID from gin context.
func GetUserID(c *gin.Context) int {
	if uid, exists := c.Get("userId"); exists {
		if v, ok := uid.(int); ok {
			return v
		}
	}
	return 0
}

// CORSMiddleware restricts cross-origin requests to the configured allow list.
// It echoes back the request Origin header only when it matches an allowed origin,
// so credentials-bearing requests (custom X-License-Id / X-User-Id headers)
// are not exposed to arbitrary websites.
func CORSMiddleware(allowedOrigins []string) gin.HandlerFunc {
	allowSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowSet[o] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowSet[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		} else if len(allowedOrigins) == 0 {
			// No origins configured — disallow all cross-origin requests.
			c.Header("Access-Control-Allow-Origin", "")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-License-Id, X-User-Id")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// IntPtr helper for request binding.
func IntPtr(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
