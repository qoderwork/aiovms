package recording

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"aiovms/pkg/utils"
)

// SegmentCompleteRequest is the payload MediaMTX's runOnRecordSegmentComplete
// hook POSTs when a recording segment has been finalized.
type SegmentCompleteRequest struct {
	// Path is the MediaMTX path name (e.g. "cam-a1b2c3d4").
	Path string `json:"path"`
	// SegmentPath is the absolute path of the completed segment file.
	SegmentPath string `json:"segment_path"`
}

// Ingester ingests a completed segment file (shared with the scanner).
type Ingester interface {
	IngestFile(path string, knownComplete bool) error
}

// HookHandler receives segment-complete callbacks from MediaMTX. It is the
// FAST ingestion path: segments appear in the recording list immediately
// after finalization, with an accurate "complete" status. The disk scanner
// remains the slow reconciliation fallback for callbacks that are lost.
//
// Registered OUTSIDE the tenant middleware: MediaMTX sends no tenant
// headers; the tenant is derived from the camera owning the path.
type HookHandler struct {
	ingester Ingester
}

func NewHookHandler(ingester Ingester) *HookHandler {
	return &HookHandler{ingester: ingester}
}

// HandleSegmentComplete godoc
// @Summary 分片完成回调（内部）
// @Description MediaMTX runOnRecordSegmentComplete 钩子回调，立即入库已完成的录像分片
// @Tags 内部接口
// @Accept json
// @Produce json
// @Param request body SegmentCompleteRequest true "分片信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Router /internal/segments/complete [post]
func (h *HookHandler) HandleSegmentComplete(c *gin.Context) {
	var req SegmentCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "invalid hook payload")
		return
	}
	if req.SegmentPath == "" {
		utils.Error(c, http.StatusBadRequest, "segment_path required")
		return
	}

	// knownComplete=true: MediaMTX has finalized the segment, so the record
	// goes straight to "complete" without the mtime heuristic.
	if err := h.ingester.IngestFile(req.SegmentPath, true); err != nil {
		// Non-fatal for the overall system (scanner reconciles), but report
		// 500 so failures are visible in MediaMTX hook logs / metrics.
		utils.Error(c, http.StatusInternalServerError, "ingest segment: "+err.Error())
		return
	}
	utils.Success(c, nil)
}
