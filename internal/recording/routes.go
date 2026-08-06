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
	}
}
