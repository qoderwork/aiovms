package recording

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"aiovms/internal/audit"
	"aiovms/internal/middleware"
	_ "aiovms/internal/model" // for swagger type resolution
	"aiovms/pkg/utils"
)

type Handler struct {
	svc            Service
	recordingsRoot string
}

func NewHandler(svc Service, recordingsRoot string) *Handler {
	return &Handler{svc: svc, recordingsRoot: recordingsRoot}
}

// ServeRecording streams a recorded .mp4 file from the local recordings volume.
//
// SELF-TEST ONLY. In production the file bytes are delivered by the integrated
// deployment layer (Java NMS backend or its nginx) from the shared recordings
// volume — aiovms only records metadata and reports the path. This route exists so
// the /recordings/files/* URL returned by Get() can be opened directly in a browser
// during local API testing (Chrome native <video> + HTTP Range → seek/scrub works
// for fMP4).
//
// Mounted at /recordings/files/*filepath (NOT /recordings/*filepath) to avoid gin's
// panic when a :param and *catchall share the same node (/recordings/:id already exists).
// Tenant isolation is intentionally NOT enforced here — this mirrors the production
// model where the static file layer is not URL-tenant-gated; the integrated layer owns auth.
func (h *Handler) ServeRecording(c *gin.Context) {
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	if rel == "" {
		c.Status(http.StatusNotFound)
		return
	}
	root := filepath.Clean(h.recordingsRoot)
	full := filepath.Clean(filepath.Join(root, rel))
	// Guard against path traversal: the resolved path must stay within root.
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		c.Status(http.StatusBadRequest)
		return
	}
	c.File(full)
}

// List godoc
// @Summary 录像列表（分页）
// @Description 按摄像头 + 时间范围分页查询录像
// @Tags 录像管理
// @Produce json
// @Param camera_id query string false "摄像头 ID"
// @Param start_time query string false "开始时间下限（RFC3339）"
// @Param end_time query string false "开始时间上限（RFC3339）"
// @Param page query int false "页码（从 1 开始）" default(1)
// @Param page_size query int false "每页数量" default(10)
// @Success 200 {object} utils.PaginatedResponse{data=[]model.Recording}
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings [get]
func (h *Handler) List(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	cameraID := c.Query("camera_id")
	startTime := c.Query("start_time")
	endTime := c.Query("end_time")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	records, total, err := h.svc.List(c.Request.Context(), tenantID, cameraID, startTime, endTime, page, pageSize)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Paginated(c, records, total, page, pageSize)
}

// Get godoc
// @Summary 录像详情
// @Description 按 ID 查询录像，返回录像信息和回放 URL
// @Tags 录像管理
// @Produce json
// @Param id path string true "录像 ID"
// @Success 200 {object} utils.Response{data=object{recording=model.Recording,play_url=string}}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings/{id} [get]
func (h *Handler) Get(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	rec, url, err := h.svc.Get(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, gin.H{"recording": rec, "play_url": url})
}

// Delete godoc
// @Summary 删除录像
// @Description 删除录像文件和数据库记录
// @Tags 录像管理
// @Produce json
// @Param id path string true "录像 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings/{id} [delete]
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")
	tenantID := int64(middleware.GetTenantID(c))
	// Fetch before delete to capture detail for audit.
	if rec, _, err := h.svc.Get(c.Request.Context(), tenantID, id); err == nil {
		audit.Write(middleware.GetUserID(c), "recording.delete", "recording", id,
			rec.Filename+" / camera:"+rec.CameraID, rec.LicenseID)
	}
	if err := h.svc.Delete(c.Request.Context(), tenantID, id); err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, nil)
}

// StartManual godoc
// @Summary 手动开始录像
// @Description 对指定摄像头立即开始手动录像
// @Tags 录像管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings/cameras/{id}/start [post]
func (h *Handler) StartManual(c *gin.Context) {
	cameraID := c.Param("id")
	if cameraID == "" {
		utils.Error(c, http.StatusBadRequest, "camera id required")
		return
	}
	tenantID := int64(middleware.GetTenantID(c))
	if err := h.svc.StartManual(c.Request.Context(), tenantID, cameraID); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "recording.start_manual", "camera", cameraID,
		"manual recording started", tenantID)
	utils.Success(c, nil)
}

// StopManual godoc
// @Summary 手动停止录像
// @Description 对指定摄像头立即停止手动录像
// @Tags 录像管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings/cameras/{id}/stop [post]
func (h *Handler) StopManual(c *gin.Context) {
	cameraID := c.Param("id")
	if cameraID == "" {
		utils.Error(c, http.StatusBadRequest, "camera id required")
		return
	}
	tenantID := int64(middleware.GetTenantID(c))
	if err := h.svc.StopManual(c.Request.Context(), tenantID, cameraID); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "recording.stop_manual", "camera", cameraID,
		"manual recording stopped", tenantID)
	utils.Success(c, nil)
}

// DeleteByCamera godoc
// @Summary 清空摄像头录像
// @Description 删除指定摄像头的所有录像（数据库记录 + 磁盘文件）
// @Tags 录像管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response{data=object{deleted=int}}
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /recordings/cameras/{id} [delete]
func (h *Handler) DeleteByCamera(c *gin.Context) {
	cameraID := c.Param("id")
	if cameraID == "" {
		utils.Error(c, http.StatusBadRequest, "camera id required")
		return
	}
	tenantID := int64(middleware.GetTenantID(c))
	count, err := h.svc.DeleteByCamera(c.Request.Context(), tenantID, cameraID)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "recording.delete_by_camera", "camera", cameraID,
		"deleted "+strconv.Itoa(count), tenantID)
	utils.Success(c, gin.H{"deleted": count})
}
