package controller

import (
	"context"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

type oauthConsentServer struct {
	ID   int64
	Name string
}

type consentPreviewItem struct {
	Capability  string
	Description string
	Status      string
	Reason      string
	RiskClass   int
}

// oauthConsentPreview evaluates the capability catalog for the consenting user
// at the requested access level so the page shows the real effective
// capabilities and exact denial reasons instead of a guessed summary.
func (s *Server) oauthConsentPreview(ctx context.Context, role model.Role, accessLevel mcpauth.AccessLevel, boundary mcpauth.ResourceBoundary) []consentPreviewItem {
	principal := application.Principal{AccessLevel: accessLevel, Role: role, Scopes: []string{"*"}}
	items := make([]consentPreviewItem, 0, len(s.capabilities.List(application.Principal{Scopes: []string{"*"}})))
	for _, descriptor := range s.capabilities.List(application.Principal{Scopes: []string{"*"}}) {
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
				} else if descriptor.RiskClass > 0 {
					item.Status, item.Reason = "requires_approval", "需要审批"
				} else {
					item.Status = "allowed"
				}
			} else {
				item.Status = "allowed"
			}
		}
		_ = boundary
		_ = principal
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
		if effective, err := s.store.EffectiveUserRole(r.Context(), *user); err == nil {
			role = effective
		}
	}
	accessLevel, offline, _ := normalizeRequestedScopes(request.Scope)
	boundary, _, _ := s.oauthConsentBoundary(r, accessLevel)
	if user != nil {
		// A GET renders with the requested level; if the user cannot grant it,
		// the page must fail loudly instead of silently downgrading.
		if err := s.validateOAuthUserGrant(r.Context(), user, request.Scope); err != nil {
			oauthError(w, http.StatusForbidden, "access_denied", err.Error())
			return
		}
	}
	preview := s.oauthConsentPreview(r.Context(), role, accessLevel, boundary)
	servers := []oauthConsentServer{}
	if items, err := s.store.ListServers(r.Context()); err == nil {
		for _, item := range items {
			servers = append(servers, oauthConsentServer{ID: item.ID, Name: item.Name})
		}
	}
	view := oauthConsentView{
		ClientName:      client.Name,
		ClientID:        client.ID,
		RedirectURI:     request.RedirectURI,
		MetadataSource:  s.clientMetadataSource(client),
		Resource:        request.Resource,
		Scopes:          request.Scope,
		AccessLevel:     string(accessLevel),
		OfflineAccess:   offline,
		Username:        "",
		Servers:         servers,
		AutoApproveRisk: "0",
		Preview:         preview,
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
.level{display:flex;flex-direction:column;gap:8px}
.level-option{border:1px solid var(--border);border-radius:8px;padding:12px 14px;display:flex;gap:10px;align-items:flex-start;cursor:pointer;transition:border-color .15s ease}
.level-option:has(input:checked){border-color:var(--primary);box-shadow:0 0 0 1px var(--primary) inset}
.level-option input{margin-top:3px}
.level-option strong{display:block;font-size:13.5px;margin-bottom:4px}
.level-option p{margin:0;color:var(--text-secondary);font-size:12px;line-height:1.6}
.warn-note{margin-top:8px;padding:8px 10px;border-radius:6px;background:var(--warn-bg);color:var(--warn);font-size:12px;line-height:1.55}
.field{display:grid;gap:7px;margin-top:10px}.field>span{font-size:12px;color:var(--text-secondary)}
select,input[type=text]{width:100%;min-height:40px;padding:8px 10px;border:1px solid var(--border-strong);border-radius:6px;background:var(--bg-inset);color:var(--text-primary);font:inherit}
select:focus-visible,input:focus-visible{outline:2px solid var(--primary);outline-offset:2px}
.check{display:flex;align-items:flex-start;gap:9px;padding:6px 0;color:var(--text-secondary);line-height:1.5;cursor:pointer}.check input{width:17px;height:17px;margin:2px 0 0;flex:0 0 auto;cursor:pointer}
.server-list{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:4px 12px;max-height:160px;overflow-y:auto;padding:10px 12px;margin:8px 0 4px;background:var(--bg-inset);border:1px solid var(--border);border-radius:6px}
.server-list .check{padding:3px 0}
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
@media(max-width:600px){body{padding:12px}.card{padding:22px 18px}.server-list{grid-template-columns:1fr}.preview-item{flex-direction:column;align-items:flex-start;gap:6px}.preview-main{width:100%;flex-direction:column;align-items:flex-start;gap:4px}.preview-desc{white-space:normal}.badge{max-width:100%;text-align:left}}
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
  <p class="sub">该客户端申请以你的账号访问 OBoard MCP 资源。客户端名称、Logo 和描述由客户端提供，可能不可信，请根据 Client ID 与回调地址确认身份。后续操作仍受你的角色、资源边界与审批策略约束。</p>
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
      <div class="row-label">访问级别</div>
      <div class="level">
        <label class="level-option"><input type="radio" name="access_level" value="read"{{if eq .AccessLevel "read"}} checked{{end}}><span><strong>只读与分析</strong><p>允许客户端读取授权范围内的 OBoard 信息、发现能力、生成计划并验证计划。客户端不能提交 Changeset、启动或控制操作 Workflow。</p></span></label>
        <label class="level-option"><input type="radio" name="access_level" value="operate"{{if eq .AccessLevel "operate"}} checked{{end}}><span><strong>管理操作（完整 MCP 权限）</strong><p>允许客户端执行当前 OBoard 用户有权执行、且位于所选资源范围内的全部 MCP 操作。所有写操作仍必须经过 Changeset、校验、版本检查和必要审批。此权限不会授予 SSH、Shell、原始 Agent Task、秘密导出、管理员删除或审批绕过能力。</p></span></label>
      </div>
      <p class="warn-note">完整 MCP 权限不等于 OBoard 管理员权限。请求 operate 时若你的角色不允许，本次授权会直接失败，不会静默降级为只读。</p>
    </div>
    <div class="row">
      <div class="row-label">服务器范围</div>
      <label class="field"><span>允许访问的服务器</span><select name="server_mode" onchange="var el=document.getElementById('server-list-box');if(el)el.style.display=this.value==='selected'?'grid':'none';"><option value="none"{{if eq .ServerMode "none"}} selected{{end}}>不允许访问服务器</option><option value="selected"{{if eq .ServerMode "selected"}} selected{{end}}>仅允许选中的服务器</option><option value="current"{{if eq .ServerMode "current"}} selected{{end}}>允许所有当前服务器</option><option value="all"{{if eq .ServerMode "all"}} selected{{end}}>允许所有当前及未来服务器</option></select></label>
      {{if .Servers}}<div id="server-list-box" class="server-list" style="display:{{if eq .ServerMode "selected"}}grid{{else}}none{{end}};">{{range .Servers}}<label class="check"><input type="checkbox" name="server_id" value="{{.ID}}"><span>{{.Name}} · #{{.ID}}</span></label>{{end}}</div>{{end}}
      <label class="check"><input type="checkbox" name="allow_create_servers" value="1"><span>允许创建新服务器并签发一次性接入动作</span></label>
    </div>
    <div class="row">
      <div class="row-label">用户范围</div>
      <label class="field"><span>允许访问的用户相关资源</span><select name="user_mode"><option value="none" selected>不允许访问用户资源</option><option value="all">允许当前及未来用户资源</option></select></label>
    </div>
    <div class="row">
      <div class="row-label">刷新令牌</div>
      <label class="check"><input type="checkbox" name="offline_access" value="1"{{if .OfflineAccess}} checked{{end}}><span>允许客户端在你离线时刷新访问令牌（offline_access）</span></label>
    </div>
    <div class="row">
      <div class="row-label">自动审批</div>
      <label class="field"><span>本 Grant 可自动批准的最高风险</span><select name="auto_approve_risk"><option value="0">不自动批准写操作</option><option value="2">Risk 2 · 普通变更</option><option value="3">Risk 3 · 受限范围内的部署</option></select></label>
      <p class="security-note">风险等级 4 永不自动审批。审批策略不会授予新的能力，也不会扩大资源边界。</p>
    </div>
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
	serverMode := strings.ToLower(strings.TrimSpace(r.Form.Get("server_mode")))
	if serverMode == "" {
		serverMode = "current"
	}
	view.ServerMode = serverMode
	if autoRisk, err := oauthAutoApproveRisk(r.Form.Get("auto_approve_risk")); err == nil {
		view.AutoApproveRisk = strconv.Itoa(autoRisk)
	}
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
	Scopes          []string
	AccessLevel     string
	OfflineAccess   bool
	ServerMode      string
	Username        string
	Servers         []oauthConsentServer
	AutoApproveRisk string
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
{{.RefreshMeta}}
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
		"RedirectURL": template.URL(location),
		"RedirectJS":  oauthJSString(location),
		"RefreshMeta": template.HTML(`<meta http-equiv="refresh" content="1.2; url=` + template.HTMLEscapeString(location) + `">`),
	})
}

func oauthJSString(value string) template.JS {
	quoted := strconv.Quote(value)
	quoted = strings.ReplaceAll(quoted, "<", `\u003c`)
	quoted = strings.ReplaceAll(quoted, ">", `\u003e`)
	quoted = strings.ReplaceAll(quoted, "&", `\u0026`)
	return template.JS(quoted)
}
