package camera

import "github.com/gin-gonic/gin"

// RegisterRoutes registers camera management REST API routes.
// Paths match the scheme document §5.7 VMS API 接口设计.
func RegisterRoutes(rg *gin.RouterGroup, h *Handler) {
	cameras := rg.Group("/cameras")
	{
		cameras.GET("", h.List)
		cameras.POST("", h.Create)
		cameras.DELETE("", h.DeleteAll)
		cameras.POST("/batch", h.BatchCreate)
		cameras.POST("/discover", h.Discover)
		cameras.GET("/status", h.Status)
		cameras.GET("/:id", h.Get)
		cameras.PUT("/:id", h.Update)
		cameras.DELETE("/:id", h.Delete)
		cameras.POST("/:id/connect", h.Connect)
		cameras.POST("/:id/disconnect", h.Disconnect)
		cameras.GET("/:id/stream-urls", h.Stream)
		cameras.GET("/:id/stream", h.StreamFLV)
		cameras.GET("/:id/stream/hls", h.StreamHLS)
		cameras.GET("/:id/stream/webrtc", h.StreamWebRTC)
		cameras.GET("/:id/snapshot", h.Snapshot)
	}
}
