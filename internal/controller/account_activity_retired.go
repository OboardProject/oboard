package controller

import "net/http"

func retiredAuditRiskEndpoint(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusGone, map[string]any{"error": "legacy_audit_scoring_retired", "message": "旧风险模型已停用，请读取账号审计快照；历史证据不参与新评分。"})
}
