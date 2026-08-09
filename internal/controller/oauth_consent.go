package controller

import (
	"html/template"
	"net/http"
	"slices"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

type consentPreviewItem struct {
	Capability  string
	Description string
	Status      string
	Reason      string
	RiskClass   int
}

// oauthConsentPreview shows the capabilities inherited from the user's current
// role. The preview is informational; it does not create a second permission
// boundary.
func (s *Server) oauthConsentPreview(role model.Role, accessLevel mcpauth.AccessLevel) []consentPreviewItem {
	items := make([]consentPreviewItem, 0)
	principal := application.Principal{AccessLevel: accessLevel, Role: role, Scopes: []string{"*"}}
	for _, descriptor := range s.capabilities.ListMCP(principal) {
		if !descriptor.MCPEnabled {
			continue
		}
		item := consentPreviewItem{Capability: descriptor.Name, Description: descriptor.Description, RiskClass: descriptor.RiskClass}
		switch {
		case !accessLevel.Allows(descriptor.MinimumAccess):
			item.Status, item.Reason = "denied_scope", "当前权限级别不足："+descriptor.MinimumAccess.RequiredScope()
		case !s.capabilities.RBAC().Allows(role, descriptor.RBACPermission):
			item.Status, item.Reason = "denied_role", "当前用户角色无权执行"
		default:
			if descriptor.Executable {
				if descriptor.RiskClass >= 4 {
					item.Status, item.Reason = "requires_approval", "风险等级 4 永不自动审批"
				} else {
					item.Status = "allowed"
				}
			} else {
				item.Status = "allowed"
			}
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b consentPreviewItem) int {
		return strings.Compare(a.Capability, b.Capability)
	})
	return items
}

func (s *Server) renderOAuthConsent(w http.ResponseWriter, r *http.Request, request oauthAuthorizationRequest, client *model.OAuthClient) {
	user := currentUser(r)
	role := model.RoleNone
	if user != nil {
		effective, err := s.store.EffectiveUserRole(r.Context(), *user)
		if err != nil {
			oauthError(w, http.StatusForbidden, "access_denied", "无法确认当前账号权限")
			return
		}
		role = effective
	}
	_, offline, _ := normalizeRequestedScopes(request.Scope)
	accessLevel := mcpAccessLevelForRole(role)
	if user != nil {
		if err := s.validateOAuthUserGrant(r.Context(), user, request.Scope); err != nil {
			oauthError(w, http.StatusForbidden, "access_denied", err.Error())
			return
		}
	}
	permissionLabel := "继承只读权限"
	if role == model.RoleAdmin {
		permissionLabel = "继承管理员权限"
	} else if role == model.RoleOperator {
		permissionLabel = "继承操作员权限"
	}
	view := oauthConsentView{
		ClientName:      client.Name,
		ClientID:        client.ID,
		RedirectURI:     request.RedirectURI,
		MetadataSource:  s.clientMetadataSource(client),
		Resource:        request.Resource,
		PermissionLabel: permissionLabel,
		OfflineAccess:   offline,
		Username:        "",
		Preview:         s.oauthConsentPreview(role, accessLevel),
		Hidden:          []map[string]string{{"Key": "client_id", "Value": request.ClientID}, {"Key": "redirect_uri", "Value": request.RedirectURI}, {"Key": "response_type", "Value": "code"}, {"Key": "scope", "Value": strings.Join(request.Scope, " ")}, {"Key": "state", "Value": request.State}, {"Key": "resource", "Value": request.Resource}, {"Key": "code_challenge", "Value": request.CodeChallenge}, {"Key": "code_challenge_method", "Value": "S256"}},
	}
	if user != nil {
		view.Username = user.Username
	}
	if sessionToken := currentSessionToken(r); sessionToken != "" {
		view.Hidden = append(view.Hidden, map[string]string{"Key": "_oboard_csrf", "Value": s.csrfTokenForSession(sessionToken)})
	}
	const page = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>OAuth 授权 · OBoard</title>
<style>
:root{color-scheme:light;--bg-page:#f7f8fa;--bg-card:#ffffff;--bg-inset:#f3f4f6;--text-primary:#111827;--text-secondary:#4b5563;--text-muted:#6b7280;--border:#eaecef;--border-strong:#e2e5ea;--primary:#111827;--primary-hover:#0b1220;--primary-contrast:#ffffff;--primary-soft:rgba(17,24,39,.08);--success:#10b981;--success-bg:rgba(16,185,129,.1);--danger:#ef4444;--danger-bg:rgba(239,68,68,.1);--warn:#b45309;--warn-bg:rgba(180,83,9,.1);--shadow:0 1px 3px rgba(0,0,0,.05),0 12px 32px rgba(15,23,42,.03)}
@media (prefers-color-scheme:dark){:root{color-scheme:dark;--bg-page:#0b0d12;--bg-card:#12151c;--bg-inset:#1a1f2b;--text-primary:#f3f4f6;--text-secondary:#c4cad4;--text-muted:#9aa3b2;--border:#2a3140;--border-strong:#3a4254;--primary:#f3f4f6;--primary-hover:#ffffff;--primary-contrast:#0b0d12;--primary-soft:rgba(243,244,246,.12);--success:#34d399;--success-bg:rgba(52,211,153,.14);--danger:#f87171;--danger-bg:rgba(248,113,113,.14);--warn:#fbbf24;--warn-bg:rgba(251,191,36,.12);--shadow:0 0 0 1px rgba(255,255,255,.04) inset,0 20px 48px rgba(0,0,0,.3)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:flex-start;justify-content:center;padding:32px 16px;background:var(--bg-page);color:var(--text-primary);font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;font-size:14px;-webkit-font-smoothing:antialiased}
.card{width:min(680px,100%);background:var(--bg-card);border:1px solid var(--border-strong);border-radius:12px;box-shadow:var(--shadow);padding:32px}
.brand{display:flex;align-items:center;gap:10px;padding-bottom:18px;margin-bottom:20px;border-bottom:1px dashed var(--border)}
.brand-mark{flex-shrink:0;width:34px;height:34px;border-radius:9px}
.brand-name{font-weight:800;font-size:15px}
.kicker{margin:0 0 8px;font-size:11.5px;font-weight:700;letter-spacing:.02em;text-transform:uppercase;color:var(--text-muted)}
h1{margin:0 0 10px;font-size:21px;font-weight:700;line-height:1.35}
.sub{margin:0 0 22px;color:var(--text-secondary);font-size:13px;line-height:1.7}
.panel{border:1px solid var(--border);border-radius:8px;padding:2px 18px;margin-top:16px}
.row{padding:13px 0}
.row+.row{border-top:1px solid var(--border)}
.row-label{margin-bottom:8px;font-size:11.5px;font-weight:700;letter-spacing:.02em;color:var(--text-muted)}
.resource{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:12.5px;line-height:1.6;word-break:break-all;color:var(--text-secondary);background:var(--bg-inset);border:1px solid var(--border);border-radius:8px;padding:8px 10px}
.preview{display:flex;flex-direction:column;gap:6px;max-height:260px;overflow-y:auto;padding:2px 4px 2px 0}
.preview-item{display:flex;align-items:center;justify-content:space-between;gap:12px;font-size:12px;padding:8px 12px;border-radius:6px;background:var(--bg-inset);border:1px solid var(--border)}
.preview-main{display:flex;align-items:center;gap:10px;min-width:0;flex:1 1 auto}
.preview-cap{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:11.5px;font-weight:600;color:var(--text-primary);background:var(--bg-card);padding:2px 7px;border-radius:4px;border:1px solid var(--border);flex-shrink:0}
.preview-desc{color:var(--text-secondary);font-size:12px;line-height:1.4;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.badge{flex-shrink:0;font-size:11px;font-weight:600;padding:3px 9px;border-radius:6px;line-height:1.4;max-width:260px;text-align:right;word-break:break-word}
.badge.ok{color:var(--success);background:var(--success-bg)}
.badge.warn{color:var(--warn);background:var(--warn-bg)}
.badge.deny{color:var(--danger);background:var(--danger-bg)}
.security-note{margin:9px 0 0;color:var(--text-muted);font-size:11.5px;line-height:1.55}
.actions{display:flex;gap:10px;justify-content:flex-end;margin-top:26px}
button{display:inline-flex;align-items:center;justify-content:center;gap:8px;min-height:40px;padding:8px 22px;border-radius:6px;border:1px solid transparent;font:inherit;font-size:13.5px;font-weight:600;cursor:pointer;transition:background .18s ease,border-color .18s ease}
button:focus-visible{outline:2px solid var(--primary);outline-offset:2px}
button.primary{background:var(--primary);color:var(--primary-contrast)}
button.primary:hover{background:var(--primary-hover)}
button.ghost{background:transparent;color:var(--text-primary);border-color:var(--border-strong)}
button.ghost:hover{background:var(--primary-soft)}
.foot{margin-top:20px;text-align:center;color:var(--text-muted);font-size:11.5px;line-height:1.7}
@media(max-width:600px){body{padding:12px}.card{padding:22px 18px}.preview-item{flex-direction:column;align-items:flex-start;gap:6px}.preview-main{width:100%;flex-direction:column;align-items:flex-start;gap:4px}.preview-desc{white-space:normal}.badge{max-width:100%;text-align:left}}
</style>
</head>
<body>
<form class="card" method="post">
  <div class="brand">
    <svg class="brand-mark" viewBox="0 0 512 512" aria-hidden="true"><rect width="512" height="512" rx="116" fill="var(--primary)"/><circle cx="256" cy="256" r="128" fill="none" stroke="var(--primary-contrast)" stroke-width="30"/><circle cx="256" cy="256" r="38" fill="var(--primary-contrast)"/></svg>
    <span class="brand-name">OBOARD</span>
  </div>
  <p class="kicker">OAuth 授权</p>
  <h1>「{{.ClientName}}」请求访问</h1>
  <p class="sub">该客户端申请以你的账号访问 OBoard MCP 资源。客户端名称、Logo 和描述由客户端提供，可能不可信，请根据 Client ID 与回调地址确认身份。</p>
  <div class="panel">
    <div class="row">
      <div class="row-label">目标资源</div>
      <div class="resource">{{.Resource}}</div>
    </div>
    <div class="row">
      <div class="row-label">客户端身份</div>
      <div class="resource">{{.ClientID}} · {{.RedirectURI}} · {{.MetadataSource}}</div>
    </div>
    <div class="row">
      <div class="row-label">继承当前账号权限</div>
      <div class="resource">{{.PermissionLabel}}</div>
      <p class="security-note">MCP 与当前账号使用同一套角色权限，不再单独限制读写或资源范围。账号角色变更会立即生效，无需重新授权。</p>
      <p class="security-note">所有写操作仍通过 Changeset、版本校验、风险审批和 Workflow 执行，不会绕过现有安全规则。</p>
    </div>
    {{if .OfflineAccess}}<div class="row">
      <div class="row-label">离线访问</div>
      <p class="security-note">客户端申请了刷新令牌，可在你离线时续期访问；吊销此授权后会立即失效。</p>
    </div>{{end}}
    <div class="row">
      <div class="row-label">实际能力预览</div>
      <div class="preview">{{range .Preview}}<div class="preview-item"><div class="preview-main"><code class="preview-cap">{{.Capability}}</code><span class="preview-desc">{{.Description}}</span></div>{{if eq .Status "allowed"}}<span class="badge ok">可执行/可读</span>{{else if eq .Status "requires_approval"}}<span class="badge warn">需审批</span>{{else}}<span class="badge deny">{{.Reason}}</span>{{end}}</div>{{end}}</div>
    </div>
  </div>
  {{range .Hidden}}<input type="hidden" name="{{.Key}}" value="{{.Value}}">{{end}}
  <div class="actions">
    <button class="ghost" type="submit" name="decision" value="deny">拒绝</button>
    <button class="primary" type="submit" name="decision" value="approve">允许授权</button>
  </div>
  <div class="foot">当前账号：{{.Username}} · 授权后可随时在 OBoard 面板吊销</div>
</form>
</body>
</html>`
	tmpl := template.Must(template.New("consent").Parse(page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = tmpl.Execute(w, view)
}

func (s *Server) clientMetadataSource(client *model.OAuthClient) string {
	switch client.IdentityType {
	case "cimd":
		if client.MetadataURI != "" {
			return "CIMD " + client.MetadataURI
		}
		return "CIMD"
	case "preregistered":
		return "预注册客户端"
	default:
		return "预注册客户端"
	}
}

type oauthConsentView struct {
	ClientName      string
	ClientID        string
	RedirectURI     string
	MetadataSource  string
	Resource        string
	PermissionLabel string
	OfflineAccess   bool
	Username        string
	Preview         []consentPreviewItem
	Hidden          []map[string]string
}

func (s *Server) renderOAuthSuccess(w http.ResponseWriter, r *http.Request, redirectURI, state, code, clientName string) {
	location := oauthRedirectLocation(redirectURI, state, code, "")
	const page = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>授权成功 · OBoard</title>
<meta http-equiv="refresh" content="1.2; url={{.RedirectURL}}">
<style>
:root{color-scheme:light;--bg-page:#f7f8fa;--bg-card:#ffffff;--text-primary:#111827;--text-secondary:#4b5563;--text-muted:#6b7280;--border:#eaecef;--border-strong:#e2e5ea;--primary:#111827;--primary-hover:#0b1220;--primary-contrast:#ffffff;--success:#10b981;--success-bg:rgba(16,185,129,.1);--shadow:0 1px 3px rgba(0,0,0,.05),0 12px 32px rgba(15,23,42,.03)}
@media (prefers-color-scheme:dark){:root{color-scheme:dark;--bg-page:#0b0d12;--bg-card:#12151c;--text-primary:#f3f4f6;--text-secondary:#c4cad4;--text-muted:#9aa3b2;--border:#2a3140;--border-strong:#3a4254;--primary:#f3f4f6;--primary-hover:#ffffff;--primary-contrast:#0b0d12;--primary-soft:rgba(243,244,246,.12);--success:#34d399;--success-bg:rgba(52,211,153,.14);--shadow:0 0 0 1px rgba(255,255,255,.04) inset,0 20px 48px rgba(0,0,0,.3)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;background:var(--bg-page);color:var(--text-primary);font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;font-size:14px;-webkit-font-smoothing:antialiased}
.card{width:min(472px,100%);background:var(--bg-card);border:1px solid var(--border-strong);border-radius:18px;box-shadow:var(--shadow);padding:32px;text-align:center}
.check{width:64px;height:64px;margin:0 auto 20px;display:flex;align-items:center;justify-content:center;border-radius:50%;background:var(--success-bg);color:var(--success)}
.kicker{margin:0 0 8px;font-size:11.5px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:var(--text-muted)}
h1{margin:0 0 10px;font-size:21px;font-weight:700;line-height:1.35}
.sub{margin:0 0 24px;color:var(--text-secondary);font-size:13px;line-height:1.7}
.fallback{color:var(--text-secondary);font-size:12.5px;text-decoration:none;border-bottom:1px dashed var(--border-strong);padding-bottom:2px}
.fallback:hover{color:var(--text-primary)}
</style>
</head>
<body>
<div class="card">
  <div class="check"><svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></div>
  <p class="kicker">OAuth 授权</p>
  <h1>授权成功</h1>
  <p class="sub">已授权「{{.ClientName}}」访问 OBoard MCP 资源，正在返回客户端……</p>
  <a class="fallback" href="{{.RedirectURL}}">未自动跳转？点击这里返回客户端</a>
</div>
<script>window.setTimeout(function(){window.location.replace({{.RedirectJS}})},1200)</script>
</body>
</html>`
	tmpl := template.Must(template.New("success").Parse(page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = tmpl.Execute(w, map[string]any{
		"ClientName":  clientName,
		"RedirectURL": location,
		"RedirectJS":  location,
	})
}
