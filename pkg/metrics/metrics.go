package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// VMS 指标定义。所有指标使用 vms_ 前缀，与 NMS 主项目的 nms_ 前缀区分。
var (
	// CamerasTotal — 摄像头总数（按状态分维）
	CameraStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vms_cameras_total",
			Help: "Total cameras by status.",
		},
		[]string{"status"},
	)

	// RecordingActiveSessions — 活跃录像会话数（按触发类型分维）
	RecordingActiveSessions = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vms_recording_active_sessions",
			Help: "Active recording sessions by trigger type (manual/schedule).",
		},
		[]string{"trigger_type"},
	)

	// MediaMTXUp — MediaMTX 健康状态 (1=up, 0=down)
	MediaMTXUp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "vms_mediamtx_up",
			Help: "MediaMTX health status (1=up, 0=down).",
		},
	)

	// MediaMTXAPIDuration — MediaMTX API 调用延迟（秒）
	MediaMTXAPIDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "vms_mediamtx_api_duration_seconds",
			Help:    "MediaMTX API call latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "status"},
	)

	// ReconcileCycleTotal — Reconciler 循环次数
	ReconcileCycleTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "vms_reconcile_cycles_total",
			Help: "Total reconciler cycles executed.",
		},
	)

	// RecordingsTotal — 录像文件总数
	RecordingsTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "vms_recordings_total",
			Help: "Total recording files in database.",
		},
	)
)

func init() {
	prometheus.MustRegister(
		CameraStatus,
		RecordingActiveSessions,
		MediaMTXUp,
		MediaMTXAPIDuration,
		ReconcileCycleTotal,
		RecordingsTotal,
	)
}
