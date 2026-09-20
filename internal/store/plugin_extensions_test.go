package store

import (
 "context"
 "encoding/json"
 "errors"
 "testing"

 "github.com/OboardProject/oboard/internal/model"
)

func TestPluginPackageUninstallRevokesExecutionAtomically(t *testing.T){
 ctx:=context.Background();s,err:=Open(t.TempDir()+"/plugins.sqlite");if err!=nil{t.Fatal(err)};defer s.Close()
 if err=s.MigratePluginExtensionsSchema(ctx);err!=nil{t.Fatal(err)}
 user:=model.User{Username:"package-admin",PasswordHash:"hash",Role:model.RoleAdmin,Status:"active",ProxyUUID:"uuid",ProxyPassword:"password"}
 if err=s.CreateUser(ctx,&user);err!=nil{t.Fatal(err)}
 metadata:=model.PluginPackageMetadata{PluginID:"test.package",Name:"Package",Version:"1.0.0"}
 rev:=model.PluginRevision{SchemaVersion:1,Runtime:model.PluginRuntimeOBoardJSv1,SDKVersion:model.PluginSDKVersionV1,Source:"function main(){}",SourceDigest:"digest",ManifestJSON:json.RawMessage(`{}`),AuthorUserID:user.ID}
 version:=model.PluginPackageVersion{Version:"1.0.0",SHA256:"digest",ManifestJSON:json.RawMessage(`{}`),SourceKind:"upload"}
 installation,version,err:=s.InstallPluginPackage(ctx,metadata,rev,version,0,0);if err!=nil{t.Fatal(err)}
 id,rid:=installation.PluginID,version.RevisionID;ts:=now()
 if _,err=s.db.ExecContext(ctx,`insert into plugin_trigger_bindings(plugin_id,revision_id,name,enabled,kind,spec_json,created_by_user_id,created_at,updated_at) values(?,?,'timer',1,'interval','{}',?,?,?)`,id,rid,user.ID,ts,ts);err!=nil{t.Fatal(err)}
 var bid int64;if err=s.db.QueryRowContext(ctx,`select id from plugin_trigger_bindings where plugin_id=?`,id).Scan(&bid);err!=nil{t.Fatal(err)}
 if _,err=s.db.ExecContext(ctx,`insert into plugin_trigger_states(binding_id,armed,next_due_at,updated_at) values(?,1,?,?)`,bid,ts,ts);err!=nil{t.Fatal(err)}
 if _,err=s.db.ExecContext(ctx,`insert into plugin_grants(plugin_id,revision_id,capabilities_json,resource_scope_json,source_digest,approved_by_user_id,created_at) values(?,?,'[]','{}','digest',?,?)`,id,rid,user.ID,ts);err!=nil{t.Fatal(err)}
 for _,status:=range []string{"queued","running","succeeded"}{
  if _,err=s.db.ExecContext(ctx,`insert into plugin_runs(uuid,plugin_id,revision_id,trigger_kind,idempotency_key,status,mode,snapshot_json,lease_owner,lease_generation,lease_until,created_at) values(?,?,?,'manual',?,?,'live','{}','worker',3,?,?)`,status,id,rid,status,status,ts,ts);err!=nil{t.Fatal(err)}
 }
 if err=s.SetPluginInstallationSecret(ctx,id,"api_key","ciphertext",installation.UpdatedAt);err!=nil{t.Fatal(err)}
 if err=s.UpdatePluginInstallationConfig(ctx,id,json.RawMessage(`{}`),installation.UpdatedAt);!errors.Is(err,ErrPluginPackageConflict){t.Fatal("stale config update accepted")}
 if err=s.ActivatePluginPackageVersion(ctx,id,rid,installation.UpdatedAt);!errors.Is(err,ErrPluginPackageConflict){t.Fatal("stale activation accepted")}
 if err=s.UninstallPluginPackage(ctx,id,false);err!=nil{t.Fatal(err)}
 assertions:=[]struct{query string;want int}{
  {`select count(*) from plugin_grants where plugin_id=? and revoked_at is null`,0},
  {`select count(*) from plugin_trigger_bindings where plugin_id=? and enabled=1`,0},
  {`select count(*) from plugin_trigger_states where binding_id in(select id from plugin_trigger_bindings where plugin_id=?) and (armed=1 or next_due_at is not null)`,0},
  {`select count(*) from plugin_runs where plugin_id=? and status in ('queued','running')`,0},
  {`select count(*) from plugin_runs where plugin_id=? and status='cancelled' and lease_owner='' and lease_until is null and lease_generation=4`,2},
  {`select count(*) from plugin_runs where plugin_id=? and status='succeeded'`,1},
  {`select count(*) from plugin_package_versions where plugin_id=?`,1},
  {`select count(*) from plugin_installation_secrets where plugin_id=?`,0},
 }
 for _,check:=range assertions{var got int;if err=s.db.QueryRowContext(ctx,check.query,id).Scan(&got);err!=nil || got!=check.want{t.Fatalf("%s: got=%d want=%d err=%v",check.query,got,check.want,err)}}
 if _,_,err=s.InstallPluginPackage(ctx,metadata,rev,version,id,0);err!=nil{t.Fatal(err)}
 var grants int;if err=s.db.QueryRowContext(ctx,`select count(*) from plugin_grants where plugin_id=? and revoked_at is null`,id).Scan(&grants);err!=nil || grants!=0{t.Fatal("reinstall revived revoked grants")}
}
