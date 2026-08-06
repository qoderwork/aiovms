package utils

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"aiovms/pkg/apperror"
	"aiovms/pkg/logger"
)

// Response is the unified VMS API response envelope.
// Code 0 = success; 5-digit business code on failure.
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// PaginatedResponse is the paginated variant of Response, used in swagger docs.
// The Data field carries a pageData structure (list/total/page/page_size).
type PaginatedResponse struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Data    pageData `json:"data,omitempty"`
}

type pageData struct {
	List     interface{} `json:"list"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: 0, Message: "Success", Data: data})
}

func Error(c *gin.Context, httpCode int, message string) {
	c.JSON(httpCode, Response{Code: httpCode * 100, Message: message})
}

// ErrorWithCode responds with a specific 5-digit business code and HTTP status.
func ErrorWithCode(c *gin.Context, httpCode, bizCode int, message string) {
	c.JSON(httpCode, Response{Code: bizCode, Message: message})
}

func Paginated(c *gin.Context, data interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "Success",
		Data: pageData{
			List:     data,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

func HandleError(c *gin.Context, err error) {
	var appErr *apperror.AppError
	if errors.As(err, &appErr) {
		ErrorWithCode(c, appErr.StatusCode, appErr.BizCode, appErr.Message)
		return
	}
	logger.Errorf("unhandled error in %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	ErrorWithCode(c, http.StatusInternalServerError, 50000, "internal server error")
}
