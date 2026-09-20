package plugin

import (
 "context"
 "encoding/json"
 "strings"
 "testing"

 "github.com/OboardProject/oboard/internal/application"
 "github.com/OboardProject/oboard/internal/model"
 "github.com/OboardProject/oboard/internal/pluginnetwork"
)

type reflectingSecretHost struct {
 recordingHost
 called bool
}
func (h *reflectingSecretHost) PluginNetworkRequest(context.Context,application.Principal,model.PluginRun,pluginnetwork.Request,pluginnetwork.Policy,string)(pluginnetwork.Response,error){
 h.called=true
 return pluginnetwork.Response{Status:200,Headers:map[string]string{"x-echo":"Bearer private-secret"},Body:`{"credential":"private-secret","encoded":"cHJpdmF0ZS1zZWNyZXQ="}`},nil
}

func TestPluginSecretNetworkResponseCannotExposeReflections(t *testing.T){
 db,svc,user:=testPluginEnv(t)
 ctx:=context.Background()
 actor:=adminActor(user)
 item,err:=svc.CreatePlugin(ctx,actor,"secret-reflection","")
 if err!=nil{t.Fatal(err)}
 policy:=pluginnetwork.Policy{AllowedOrigins:[]string{"https://example.com"},AllowedMethods:[]string{"POST"}}
 manifest:=model.PluginManifest{SchemaVersion:model.PluginSchemaVersion,Runtime:model.PluginRuntimeOBoardJSv1,SDKVersion:model.PluginSDKVersionV1,Entry:"main",Capabilities:[]string{SDKNetworkRequest},Secrets:[]model.PluginSecretRef{{Name:"api_key",Purpose:"network"}},Network:&model.PluginNetworkPolicy{AllowedOrigins:policy.AllowedOrigins,AllowedMethods:policy.AllowedMethods}}
 revision,err:=svc.SaveDraft(ctx,actor,item.ID,"function main(){}",MustJSON(manifest))
 if err!=nil{t.Fatal(err)}
 revision,err=svc.Publish(ctx,actor,item.ID,revision.ID)
 if err!=nil{t.Fatal(err)}
 grant,err:=svc.CreateGrant(ctx,actor,model.PluginGrant{RevisionID:revision.ID,CapabilitiesJSON:MustJSON(manifest.Capabilities),ConstraintsJSON:MustJSON(map[string]any{"network":policy,"secrets":[]string{"api_key"}})})
 if err!=nil{t.Fatal(err)}
 host:=&reflectingSecretHost{}
 gateway:=NewGateway(db,host)
 run:=model.PluginRun{PluginID:item.ID,RevisionID:revision.ID,GrantID:&grant.ID}
 result,err:=gateway.dispatch(ctx,application.Principal{},run,model.PluginRunAction{Capability:SDKNetworkRequest},sdkArgs{Secret:"api_key",Request:&pluginnetwork.Request{URL:"https://example.com/send",Method:"POST"}})
 if err!=nil || !host.called || result["status"]!=200{t.Fatalf("network result: %#v %v",result,err)}
 encoded,_:=json.Marshal(result)
 if strings.Contains(string(encoded),"private-secret") || strings.Contains(string(encoded),"cHJpdmF0ZS1zZWNyZXQ=") || result["body"]!="" {t.Fatalf("secret response content escaped: %s",encoded)}
 host.called=false
 if _,err:=gateway.dispatch(ctx,application.Principal{},run,model.PluginRunAction{Capability:SDKNetworkRequest},sdkArgs{Secret:"other_key",Request:&pluginnetwork.Request{URL:"https://example.com/send",Method:"POST"}});err==nil || host.called {t.Fatal("undeclared secret reached network host")}
}
