// Package audit provides asynchronous audit log writing for VMS operations.
// Logs are written to the vms_audit_log table via a shared *gorm.DB instance.
//
// Usage:
//
//	audit.Init(db)   // once at startup
//	audit.Write(uid, "camera.create", "camera", camID, "cam-01 / 10.0.0.1", licenseID)
package audit

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"aiovms/internal/model"
	"aiovms/pkg/logger"
)

var db *gorm.DB

// Init stores the database handle for later use by Write.
// Must be called once at startup.
func Init(gormDB *gorm.DB) {
	db = gormDB
}

// Write inserts an audit log entry. It runs the DB write in a new goroutine
// so the caller's response is not delayed. Errors are logged at Warn level
// and never returned — audit failure must not block business operations.
func Write(userID int, action, target, targetID, detail string, licenseID int64) {
	if db == nil {
		logger.Warn("audit: db not initialized, skip write")
		return
	}

	entry := &model.VMSAuditLog{
		ID:        uuid.NewString(),
		UserID:    userID,
		Username:  fmt.Sprintf("user_%d", userID),
		Action:    action,
		Target:    target,
		TargetID:  targetID,
		Detail:    detail,
		LicenseID: licenseID,
		CreatedAt: time.Now(),
	}

	// Fire-and-forget; audit is best-effort.
	go func() {
		if err := db.Create(entry).Error; err != nil {
			logger.Warnf("audit: write %s/%s failed: %v", action, targetID, err)
		}
	}()
}
