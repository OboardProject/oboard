export function parseJSONLoose(raw: any) {
  if (!raw) return null
  if (typeof raw === 'object') return raw
  try { return JSON.parse(String(raw)) } catch { return String(raw) }
}
