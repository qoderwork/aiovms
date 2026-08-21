package recording

import "github.com/gin-gonic/gin"

func RegisterRoutes(rg *gin.RouterGroup, h *Handler) {
	recordings := rg.Group("/recordings")
	{
		recordings.GET("", h.List)
		recordings.GET("/:id", h.Get)
		recordings.DELETE("/:id", h.Delete)
		// Manual start/stop are recording operations nested under /recordings/cameras.
		recordings.POST("/cameras/:id/start", h.StartManual)
		recordings.POST("/cameras/:id/stop", h.StopManual)
		recordings.DELETE("/cameras/:id", h.DeleteByCamera)
	}
}

// RegisterPublicRoutes registers routes that must bypass tenant middleware.
// Only the self-test file serving route belongs here — it serves recorded .mp4
// files so the playback URL returned by Get() is directly openable in a browser
// (Chrome <video> + HTTP Range → seek/scrub works for fMP4). Production serves
// via the integrated deployment layer (Java NMS backend / its nginx), not aiovms.
func RegisterPublicRoutes(router *gin.Engine, h *Handler) {
	router.GET("/recordings/files/*filepath", h.ServeRecording)
}
