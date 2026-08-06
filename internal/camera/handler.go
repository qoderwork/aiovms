package camera

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"aiovms/internal/audit"
	"aiovms/internal/middleware"
	"aiovms/internal/model"
	"aiovms/internal/onvif"
	"aiovms/pkg/utils"
)

type Handler struct {
	svc Service
}

// CreateCameraRequest is the DTO for creating a new camera.
// Fields are restricted to user-supplied inputs only; system-managed
// fields (id, status, timestamps, license_id, media_mtx_path, codec,
// resolution, fps, password_enc) are explicitly excluded so that
// JSON binding cannot inject them.
type CreateCameraRequest struct {
	Name          string   `json:"name"`
	IP            string   `json:"ip"`
	Port          int      `json:"port"`
	Protocol      string   `json:"protocol"` // RTSP / ONVIF
	Username      string   `json:"username"`
	Password      string   `json:"password,omitempty"`
	StreamURL     string   `json:"stream_url"`
	SubStreamURL  string   `json:"sub_stream_url"`
	Manufacturer  string   `json:"manufacturer"`
	Model         string   `json:"model"`
	SiteID        *string  `json:"site_id"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
}

// UpdateCameraRequest is the DTO for updating an existing camera.
// Same field restrictions as CreateCameraRequest.
type UpdateCameraRequest struct {
	Name          string   `json:"name"`
	IP            string   `json:"ip"`
	Port          int      `json:"port"`
	Protocol      string   `json:"protocol"`
	Username      string   `json:"username"`
	Password      string   `json:"password,omitempty"`
	StreamURL     string   `json:"stream_url"`
	SubStreamURL  string   `json:"sub_stream_url"`
	Manufacturer  string   `json:"manufacturer"`
	Model         string   `json:"model"`
	SiteID        *string  `json:"site_id"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
}

// toCamera copies DTO fields into a model.Camera, leaving system-managed
// fields (ID, Status, LicenseID, timestamps, codec/resolution/fps,
// PasswordEnc, MediaMTXPath, DeletedAt) zeroed so service layer can set them.
func (r *CreateCameraRequest) toCamera() model.Camera {
	return model.Camera{
		Name:         r.Name,
		IP:           r.IP,
		Port:         r.Port,
		Protocol:     r.Protocol,
		Username:     r.Username,
		Password:     r.Password,
		StreamURL:    r.StreamURL,
		SubStreamURL: r.SubStreamURL,
		Manufacturer: r.Manufacturer,
		Model:        r.Model,
		SiteID:       r.SiteID,
		Latitude:     r.Latitude,
		Longitude:    r.Longitude,
	}
}

// toCamera copies DTO fields into a model.Camera for update flow.
func (r *UpdateCameraRequest) toCamera() model.Camera {
	return model.Camera{
		Name:         r.Name,
		IP:           r.IP,
		Port:         r.Port,
		Protocol:     r.Protocol,
		Username:     r.Username,
		Password:     r.Password,
		StreamURL:    r.StreamURL,
		SubStreamURL: r.SubStreamURL,
		Manufacturer: r.Manufacturer,
		Model:        r.Model,
		SiteID:       r.SiteID,
		Latitude:     r.Latitude,
		Longitude:    r.Longitude,
	}
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary 摄像头列表（分页）
// @Description 按租户分页查询摄像头，支持名称/IP模糊搜索
// @Tags 摄像头管理
// @Accept json
// @Produce json
// @Param q query string false "名称或 IP 关键字"
// @Param page query int false "页码（从 1 开始）" default(1)
// @Success 200 {object} utils.PaginatedResponse{data=[]model.Camera}
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras [get]
func (h *Handler) List(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	query := c.Query("q")
	page := parsePage(c)

	cams, total, err := h.svc.List(c.Request.Context(), tenantID, query, page, 10)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Paginated(c, cams, total, page, 10)
}

// Create godoc
// @Summary 创建摄像头
// @Description 创建摄像头并注册到 MediaMTX，status 初始为 connecting
// @Tags 摄像头管理
// @Accept json
// @Produce json
// @Param request body CreateCameraRequest true "摄像头参数（含 password 明文）"
// @Success 200 {object} utils.Response{data=model.Camera}
// @Failure 400 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras [post]
func (h *Handler) Create(c *gin.Context) {
	var req CreateCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cam := req.toCamera()
	cam.LicenseID = int64(middleware.GetTenantID(c))

	if err := h.svc.Create(c.Request.Context(), &cam); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "camera.create", "camera", cam.ID,
		cam.Name+" / "+cam.IP, cam.LicenseID)
	utils.Success(c, cam)
}

// Get godoc
// @Summary 摄像头详情
// @Description 按 ID 查询单个摄像头
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response{data=model.Camera}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id} [get]
func (h *Handler) Get(c *gin.Context) {
	cam, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, cam)
}

// Update godoc
// @Summary 更新摄像头
// @Description 更新摄像头参数，可选重置 password
// @Tags 摄像头管理
// @Accept json
// @Produce json
// @Param id path string true "摄像头 ID"
// @Param request body UpdateCameraRequest true "摄像头参数"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id} [put]
func (h *Handler) Update(c *gin.Context) {
	var req UpdateCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	id := c.Param("id")
	cam := req.toCamera()
	if err := h.svc.Update(c.Request.Context(), id, &cam); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "camera.update", "camera", id,
		req.Name+" / "+req.IP, int64(middleware.GetTenantID(c)))
	utils.Success(c, nil)
}

// Delete godoc
// @Summary 删除摄像头
// @Description 软删除摄像头并从 MediaMTX 注销
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id} [delete]
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")
	cam, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "camera.delete", "camera", id,
		cam.Name+" / "+cam.IP, cam.LicenseID)
	utils.Success(c, nil)
}

// Connect godoc
// @Summary 连接摄像头
// @Description 触发与 ONVIF/RTSP 设备的连接
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/connect [post]
func (h *Handler) Connect(c *gin.Context) {
	if err := h.svc.Connect(c.Request.Context(), c.Param("id")); err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, nil)
}

// Disconnect godoc
// @Summary 断开摄像头
// @Description 断开与设备的连接
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/disconnect [post]
func (h *Handler) Disconnect(c *gin.Context) {
	if err := h.svc.Disconnect(c.Request.Context(), c.Param("id")); err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, nil)
}

// Stream godoc
// @Summary 获取全部流地址
// @Description 返回 FLV/HLS/WebRTC 三种播放地址（一期前端默认使用 HTTP-FLV）
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response{data=camera.StreamURLs}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/stream-urls [get]
func (h *Handler) Stream(c *gin.Context) {
	urls, err := h.svc.GetStreamURLs(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, urls)
}

// StreamFLV godoc
// @Summary 获取 HTTP-FLV 播放地址
// @Description 返回 MediaMTX 的 HTTP-FLV 地址（一期默认启用）
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} object{url=string}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/stream [get]
func (h *Handler) StreamFLV(c *gin.Context) {
	urls, err := h.svc.GetStreamURLs(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, gin.H{"url": urls.FLV})
}

// StreamHLS godoc
// @Summary 获取 HLS 播放地址（二期）
// @Description 返回 MediaMTX 的 HLS 地址，二期开放
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} object{url=string}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/stream/hls [get]
func (h *Handler) StreamHLS(c *gin.Context) {
	urls, err := h.svc.GetStreamURLs(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, gin.H{"url": urls.HLS})
}

// StreamWebRTC godoc
// @Summary 获取 WebRTC 播放地址（二期）
// @Description 返回 MediaMTX 的 WebRTC 地址，二期开放
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} object{url=string}
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/stream/webrtc [get]
func (h *Handler) StreamWebRTC(c *gin.Context) {
	urls, err := h.svc.GetStreamURLs(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, gin.H{"url": urls.WebRTC})
}

// Discover godoc
// @Summary ONVIF 设备发现
// @Description 通过 WS-Discovery 在本网段探测 ONVIF 设备
// @Tags 摄像头管理
// @Accept json
// @Produce json
// @Param request body object false "可选，timeout_sec 缺省 5 秒"
// @Success 200 {object} utils.Response{data=[]onvif.DiscoveredDevice}
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/discover [post]
func (h *Handler) Discover(c *gin.Context) {
	var req struct {
		TimeoutSec int `json:"timeout_sec"`
	}
	// Body is optional; default 5s when empty or field missing.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			utils.Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	devices, err := h.svc.Discover(c.Request.Context(), timeoutSec)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	if devices == nil {
		devices = []onvif.DiscoveredDevice{}
	}
	utils.Success(c, devices)
}

// Snapshot godoc
// @Summary 抓拍快照
// @Description 触发设备抓拍并返回快照信息
// @Tags 摄像头管理
// @Produce json
// @Param id path string true "摄像头 ID"
// @Success 200 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/{id}/snapshot [get]
func (h *Handler) Snapshot(c *gin.Context) {
	result, err := h.svc.Snapshot(c.Request.Context(), c.Param("id"))
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, result)
}

// Status godoc
// @Summary 摄像头状态列表
// @Description 返回所有摄像头的轻量级状态，供 NMS 订阅
// @Tags 摄像头管理
// @Produce json
// @Success 200 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/status [get]
func (h *Handler) Status(c *gin.Context) {
	statuses, err := h.svc.ListStatuses(c.Request.Context())
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	utils.Success(c, statuses)
}

// DeleteAll godoc
// @Summary 清空所有摄像头
// @Description 删除当前租户下所有摄像头并批量注销 MediaMTX 路径
// @Tags 摄像头管理
// @Produce json
// @Success 200 {object} utils.Response{data=object{deleted=int64}}
// @Failure 500 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras [delete]
func (h *Handler) DeleteAll(c *gin.Context) {
	tenantID := int64(middleware.GetTenantID(c))
	count, err := h.svc.DeleteAll(c.Request.Context(), tenantID)
	if err != nil {
		utils.HandleError(c, err)
		return
	}
	audit.Write(middleware.GetUserID(c), "camera.delete_all", "cameras", "",
		"deleted "+fmt.Sprintf("%d", count), tenantID)
	utils.Success(c, gin.H{"deleted": count})
}

func parsePage(c *gin.Context) int {
	p := 1
	if v := c.Query("page"); v != "" {
		if n := parseIntSafe(v); n > 0 {
			p = n
		}
	}
	return p
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---------------------------------------------------------------------------
// Batch import
// ---------------------------------------------------------------------------

// BatchCreateRequest wraps a list of single-camera create payloads.
type BatchCreateRequest struct {
	Cameras []CreateCameraRequest `json:"cameras" binding:"required,min=1,max=100"`
}

// batchResult is a per-row outcome.
type batchResult struct {
	Index    int    `json:"index"`
	Success  bool   `json:"success"`
	CameraID string `json:"camera_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BatchCreate godoc
// @Summary 批量导入摄像头
// @Description 批量创建摄像头，每行独立处理，部分失败不影响其他行
// @Tags 摄像头管理
// @Accept json
// @Produce json
// @Param request body BatchCreateRequest true "摄像头列表（1~100 条）"
// @Success 200 {object} utils.Response{data=[]batchResult}
// @Failure 400 {object} utils.Response
// @Security TenantHeader
// @Security UserHeader
// @Router /cameras/batch [post]
func (h *Handler) BatchCreate(c *gin.Context) {
	var req BatchCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	tenantID := int64(middleware.GetTenantID(c))
	userID := middleware.GetUserID(c)
	results := make([]batchResult, 0, len(req.Cameras))

	for i, camReq := range req.Cameras {
		cam := camReq.toCamera()
		cam.LicenseID = tenantID

		if err := h.svc.Create(c.Request.Context(), &cam); err != nil {
			results = append(results, batchResult{Index: i, Success: false, Error: err.Error()})
			continue
		}
		results = append(results, batchResult{Index: i, Success: true, CameraID: cam.ID})
		audit.Write(userID, "camera.create", "camera", cam.ID,
			cam.Name+" / "+cam.IP, tenantID)
	}
	utils.Success(c, results)
}
