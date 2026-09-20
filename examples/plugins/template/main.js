function main() {
  var result = oboard.state.get({ key: "greeting" });
  return { message: result.result.exists ? result.result.value : "你好，OBoard" };
}
