function main() {
  var ids = params.server_ids.split(",").map(function (id) { return id.trim(); });
  if (ids.length < 1 || ids.length > 5) throw { error_code: "invalid_input", message: "请选择 1–5 个节点" };
  var seen = {};
  ids.forEach(function (id) {
    if (!/^[1-9][0-9]*$/.test(id) || seen[id]) throw { error_code: "invalid_input", message: "节点 ID 必须有效且不重复" };
    seen[id] = true;
  });
  var rows = ids.map(function (id) {
    var server = oboard.servers.get({ server_id: id }).result;
    return { server_id: id, name: server.name, service: params.service, status: "待确认" };
  });
  if (params.confirm !== true) return { preview: true, rows: rows };
  rows.forEach(function (row) {
    try {
      var action = oboard.services.restart({ server_id: row.server_id, service: params.service });
      row.operation_id = action.operation_id || action.result.operation_id;
      row.status = action.result.simulated ? "模拟，未下发" : "已接受，待节点确认";
      if (!action.result.simulated && row.operation_id) {
        var operation = oboard.operations.get({ operation_id: row.operation_id }).result;
        row.operation_status = operation.task_status || operation.status;
      }
    } catch (error) {
      row.status = "请求失败或结果未知，请先查执行记录";
      row.error_code = error.error_code || "internal_error";
    }
  });
  return { preview: false, rows: rows };
}
