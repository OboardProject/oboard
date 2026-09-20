function main() {
  var response = oboard.network.request({
    request: {
      url: "https://example.com/notify",
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message: params.message }),
      timeout_seconds: 10
    },
    secret: "notify_token"
  });
  if (response.result.simulated) return { simulated: true };
  var status = response.result.status;
  log.info(status >= 200 && status < 300 ? "通知服务已接受请求" : "通知服务未接受请求");
  return { accepted: status >= 200 && status < 300, status: status };
}
