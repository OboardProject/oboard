function main() {
  var status = oboard.servers.status({ server_id: params.server_id }).result;
  var metrics = oboard.metrics.latest({ server_id: params.server_id }).result;
  var findings = [];
  if (status.stale) findings.push("控制链路观测已过期，请核对节点状态");
  if (status.config_applied === "false") findings.push("配置尚未确认同步");
  if (!metrics.exists || metrics.stale) {
    findings.push("缺少新鲜资源样本，不能判断节点负载");
  } else {
    if (metrics.cpu_usage_percent >= 90) findings.push("CPU 使用率达到 90%");
    if (metrics.memory_total_bytes > 0 && metrics.memory_used_bytes / metrics.memory_total_bytes >= 0.9) {
      findings.push("内存使用率达到 90%");
    }
  }
  var diagnostic = { requested: false };
  if (params.diagnose === true) {
    var action = oboard.services.status({ server_id: params.server_id, service: "oboard-sb" });
    diagnostic = { requested: true, simulated: action.result.simulated === true };
    if (!diagnostic.simulated) {
      diagnostic.operation_id = action.operation_id || action.result.operation_id;
      diagnostic.receipt = oboard.operations.get({ operation_id: diagnostic.operation_id }).result;
    }
  }
  log.info("节点巡检完成；服务任务结果以执行记录为准");
  return {
    label: env.REPORT_LABEL,
    server_id: params.server_id,
    status: status,
    metrics: metrics,
    findings: findings,
    diagnostic: diagnostic
  };
}
