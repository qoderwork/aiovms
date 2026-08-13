package schedule

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"aiovms/internal/audit"
	"aiovms/internal/middleware"
	"aiovms/internal/model"
	"aiovms/pkg/utils"
)

// ---------------------------------------------------------------------------
// DTOs — restrict JSON binding to user-supplied fields only.
// System-managed fields (ID, LicenseID, LastAction, LastTriggeredAt,
// CreatedAt, UpdatedAt) are excluded and set by handler / service.
// ---------------------------------------------------------------------------

// CreateScheduleRequest is the DTO for creating a recording schedule.
type CreateScheduleRequest struct {
	CameraID   string `json:"camera_id" binding:"required"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Weekdays   string `json:"weekdays"`    // "1,2,3,4,5" (Sun=0)
	StreamType string `json:"stream_type"` // main / sub — 一期预留，暂不生效，录像始终用主码流
	StartTime  string `json:"start_time"`  // "08:00"
	EndTime    string `json:"end_time"`    // "20:00"
}

// UpdateScheduleRequest is the DTO for updating a recording schedule.
type UpdateScheduleRequest struct {
	CameraID   string `json:"camera_id" binding:"required"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Weekdays   string `json:"weekdays"`
	StreamType string `json:"stream_type"` // main / sub — 一期预留，暂不生效
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
}

func (r *CreateScheduleRequest) toRecordSchedule() model.RecordSchedule {
	return model.RecordSchedule{
		CameraID:   r.CameraID,
		Name:       r.Name,
		Enabled:    r.Enabled,
		Weekdays:   r.Weekdays,
		StreamType: r.StreamType,
		StartTime:  r.StartTime,
		EndTime:    r.EndTime,
	}
}

func (r *UpdateScheduleRequest) toRecordSchedule() model.RecordSchedule {
	return model.RecordSchedule{
		CameraID:   r.CameraID,
		Name:       r.Name,
		Enabled:    r.Enabled,
		Weekdays:   r.Weekdays,
		StreamType: r.StreamType,
		StartTime:  r.StartTime,
		EndTime:    r.EndTime,
	}
}

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary 录像计划列表
// @Description 按租户查询录像计划，可选按摄像头过滤
// @Tags 录像计划
// @Produce json
// @Param camera_id query string false "摄像头 ID"
// @Success 200 {object} utils.Response{data=[]model.RecordSchedule}
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /schedules [get]
func (h *Handler) List(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	cameraID := c.Query("camera_id")
	schedules, err := h.svc.List(c.Request.Context(), tenantID, cameraID)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, schedules)
}

// Create godoc
// @Summary 创建录像计划
// @Description 为摄像头创建周期性录像计划
// @Tags 录像计划
// @Accept json
// @Produce json
// @Param request body CreateScheduleRequest true "录像计划参数"
// @Success 200 {object} utils.Response{data=model.RecordSchedule}
// @Failure 400 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /schedules [post]
func (h *Handler) Create(c *gin.Context) {
	var req CreateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	sch := req.toRecordSchedule()
	sch.LicenseID = int64(middleware.GetTenantID(c))
	if err := h.svc.Create(c.Request.Context(), &sch); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "schedule.create", "schedule", sch.ID,
		sch.Name+" / camera:"+sch.CameraID, sch.LicenseID)
	utils.Success(c, sch)
}

// Update godoc
// @Summary 更新录像计划
// @Description 更新指定录像计划的参数
// @Tags 录像计划
// @Accept json
// @Produce json
// @Param id path string true "计划 ID"
// @Param request body UpdateScheduleRequest true "录像计划参数"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /schedules/{id} [put]
func (h *Handler) Update(c *gin.Context) {
	var req UpdateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	id := c.Param("id")
	tenantID := int64(middleware.GetTenantID(c))
	sch := req.toRecordSchedule()
	if err := h.svc.Update(c.Request.Context(), tenantID, id, &sch); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "schedule.update", "schedule", id,
		sch.Name+" / camera:"+sch.CameraID, tenantID)
	utils.Success(c, nil)
}

// Delete godoc
// @Summary 删除录像计划
// @Description 删除指定录像计划
// @Tags 录像计划
// @Produce json
// @Param id path string true "计划 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /schedules/{id} [delete]
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")
	tenantID := int64(middleware.GetTenantID(c))
	// Fetch before delete to capture detail for audit.
	if sch, err := h.svc.Get(c.Request.Context(), tenantID, id); err == nil {
		audit.Write(middleware.GetUserID(c), "schedule.delete", "schedule", id,
			sch.Name+" / camera:"+sch.CameraID, sch.LicenseID)
	}
	if err := h.svc.Delete(c.Request.Context(), tenantID, id); err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, nil)
}

// Toggle godoc
// @Summary 启停录像计划
// @Description 翻转录像计划的 enabled 标志
// @Tags 录像计划
// @Produce json
// @Param id path string true "计划 ID"
// @Success 200 {object} utils.Response{data=model.RecordSchedule}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /schedules/{id}/toggle [patch]
func (h *Handler) Toggle(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	sch, err := h.svc.Toggle(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	action := "schedule.disable"
	if sch.Enabled {
		action = "schedule.enable"
	}
	audit.Write(middleware.GetUserID(c), action, "schedule", sch.ID,
		sch.Name+" / camera:"+sch.CameraID, sch.LicenseID)
	utils.Success(c, sch)
}
