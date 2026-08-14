package model

import (
	"fmt"
	"net/url"
	"time"

	"aiovms/pkg/crypto"
)

// Camera represents a camera registered in the VMS system.
type Camera struct {
	ID           string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Name         string     `gorm:"column:name;type:varchar(128);uniqueIndex:uk_license_name" json:"name"`
	IP           string     `gorm:"column:ip;type:varchar(64);uniqueIndex:uk_license_ip_port" json:"ip"`
	Port         int        `gorm:"column:port;uniqueIndex:uk_license_ip_port" json:"port"`
	Protocol     string     `gorm:"column:protocol;type:varchar(32)" json:"protocol"` // RTSP / ONVIF
	Username     string     `gorm:"column:username;type:varchar(64)" json:"username"`
	PasswordEnc  string     `gorm:"column:password_enc;type:varchar(512)" json:"-"`
	Password     string     `gorm:"-" json:"-"` // plaintext received from request, never persisted nor returned
	StreamURL    string     `gorm:"column:stream_url;type:varchar(512);uniqueIndex:uk_license_stream_url" json:"stream_url"`
	SubStreamURL string     `gorm:"column:sub_stream_url;type:varchar(512)" json:"sub_stream_url"`
	Status       string     `gorm:"column:status;type:varchar(32)" json:"status"` // online / offline / connecting / disconnected / error
	Resolution   string     `gorm:"column:resolution;type:varchar(32)" json:"resolution"`
	FPS          int        `gorm:"column:fps" json:"fps"`
	Codec        string     `gorm:"column:codec;type:varchar(32)" json:"codec"` // H.264 / H.265
	Manufacturer string     `gorm:"column:manufacturer;type:varchar(64)" json:"manufacturer"`
	Model        string     `gorm:"column:model;type:varchar(128)" json:"model"`
	SiteID       *string    `gorm:"column:site_id;type:varchar(32)" json:"site_id"`
	Latitude     *float64   `gorm:"column:latitude;type:decimal(10,6)" json:"latitude"`
	Longitude    *float64   `gorm:"column:longitude;type:decimal(10,6)" json:"longitude"`
	MediaMTXPath string     `gorm:"column:media_mtx_path;type:varchar(128)" json:"media_mtx_path"`
	LicenseID    int64      `gorm:"column:license_id;uniqueIndex:uk_license_name;uniqueIndex:uk_license_ip_port;uniqueIndex:uk_license_stream_url" json:"license_id"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt    *time.Time `gorm:"column:deleted_at;index" json:"deleted_at,omitempty"`
}

func (Camera) TableName() string { return "cameras" }

// SourceURL returns the RTSP source URL used to register the camera with
// MediaMTX. When Username is set, the decrypted password is injected into the
// URL userinfo (rtsp://user:pass@host/path); anonymous cameras (no username)
// return StreamURL unchanged.
func (c *Camera) SourceURL() (string, error) {
	if c.Username == "" {
		return c.StreamURL, nil
	}

	password := ""
	if c.PasswordEnc != "" {
		p, err := crypto.Decrypt(c.PasswordEnc)
		if err != nil {
			return "", fmt.Errorf("decrypt camera password: %w", err)
		}
		password = p
	}

	u, err := url.Parse(c.StreamURL)
	if err != nil {
		return "", fmt.Errorf("parse stream_url: %w", err)
	}
	if password != "" {
		u.User = url.UserPassword(c.Username, password)
	} else {
		u.User = url.User(c.Username)
	}
	return u.String(), nil
}

// Recording represents a recorded video file on disk.
type Recording struct {
	ID           string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	CameraID     string    `gorm:"column:camera_id;type:varchar(36);index" json:"camera_id"`
	SessionID    *string   `gorm:"column:session_id;type:varchar(36);index" json:"session_id,omitempty"` // link to RecordingSession; nullable for legacy files
	MediaMTXPath string    `gorm:"column:media_mtx_path;type:varchar(128);index" json:"media_mtx_path"`  // MediaMTX path name (e.g. "cam-a1b2c3d4"), used for playback URL
	Filename     string    `gorm:"column:filename;type:varchar(256)" json:"filename"`
	FilePath     string    `gorm:"column:file_path;type:varchar(512);uniqueIndex" json:"file_path"`
	FileSize     int64     `gorm:"column:file_size" json:"file_size"`
	StartTime    time.Time `gorm:"column:start_time" json:"start_time"`
	EndTime      time.Time `gorm:"column:end_time" json:"end_time"`
	Duration     int       `gorm:"column:duration" json:"duration"`
	Codec        string    `gorm:"column:codec;type:varchar(32)" json:"codec"`
	Resolution   string    `gorm:"column:resolution;type:varchar(32)" json:"resolution"`
	Status       string    `gorm:"column:status;type:varchar(32)" json:"status"`           // recording / complete / truncated
	RecordType   string    `gorm:"column:record_type;type:varchar(32)" json:"record_type"` // reserved; currently all "scheduled"
	LicenseID    int64     `gorm:"column:license_id" json:"license_id"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Recording) TableName() string { return "recordings" }

// RecordingSession represents a recording intent: one Start → Stop lifecycle.
// Multiple mp4 segments (Recording rows) produced during the session share the
// same SessionID. end_time IS NULL means the session is active (currently recording).
// This is the source of truth for restoring recording state after MediaMTX restart.
type RecordingSession struct {
	ID          string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	CameraID    string     `gorm:"column:camera_id;type:varchar(36);index" json:"camera_id"`
	TriggerType string     `gorm:"column:trigger_type;type:varchar(16)" json:"trigger_type"`               // manual / schedule
	ScheduleID  *string    `gorm:"column:schedule_id;type:varchar(36);index" json:"schedule_id,omitempty"` // set when trigger_type=schedule
	StartTime   time.Time  `gorm:"column:start_time" json:"start_time"`
	EndTime     *time.Time `gorm:"column:end_time;index" json:"end_time,omitempty"` // NULL = active
	LicenseID   int64      `gorm:"column:license_id" json:"license_id"`
	CreatedAt   time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (RecordingSession) TableName() string { return "recording_sessions" }

// RecordSchedule defines a recurring recording plan for a camera.
type RecordSchedule struct {
	ID              string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	CameraID        string     `gorm:"column:camera_id;type:varchar(36);index" json:"camera_id"`
	Name            string     `gorm:"column:name;type:varchar(128)" json:"name"`
	Enabled         bool       `gorm:"column:enabled" json:"enabled"`
	Weekdays        string     `gorm:"column:weekdays;type:varchar(32)" json:"weekdays"`       // "1,2,3,4,5" (Sun=0)
	StreamType      string     `gorm:"column:stream_type;type:varchar(32)" json:"stream_type"` // main / sub — 一期预留，暂不生效，录像始终用主码流
	StartTime       string     `gorm:"column:start_time;type:time" json:"start_time"`          // "08:00"
	EndTime         string     `gorm:"column:end_time;type:time" json:"end_time"`              // "20:00"
	LastTriggeredAt *time.Time `gorm:"column:last_triggered_at" json:"last_triggered_at"`
	LastAction      string     `gorm:"column:last_action;type:varchar(16)" json:"last_action"` // start / stop / idle
	LicenseID       int64      `gorm:"column:license_id" json:"license_id"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (RecordSchedule) TableName() string { return "record_schedules" }

// VMSAuditLog records operations on cameras and recordings for audit.
type VMSAuditLog struct {
	ID        string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	UserID    int       `gorm:"column:user_id" json:"user_id"`
	Username  string    `gorm:"column:username;type:varchar(128)" json:"username"`
	Action    string    `gorm:"column:action;type:varchar(64)" json:"action"`
	Target    string    `gorm:"column:target;type:varchar(64)" json:"target"` // camera / recording / schedule
	TargetID  string    `gorm:"column:target_id;type:varchar(36)" json:"target_id"`
	Detail    string    `gorm:"column:detail;type:text" json:"detail"`
	LicenseID int64     `gorm:"column:license_id" json:"license_id"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (VMSAuditLog) TableName() string { return "vms_audit_log" }
