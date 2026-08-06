package schedule

import "github.com/gin-gonic/gin"

func RegisterRoutes(rg *gin.RouterGroup, h *Handler) {
	schedules := rg.Group("/schedules")
	{
		schedules.GET("", h.List)
		schedules.POST("", h.Create)
		schedules.PUT("/:id", h.Update)
		schedules.DELETE("/:id", h.Delete)
		schedules.PATCH("/:id/toggle", h.Toggle)
	}
}
