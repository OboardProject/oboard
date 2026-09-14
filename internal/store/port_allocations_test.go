package store

// legacyProxyPathPortAllocationSchema is the table as released before the
// generation-aware ledger: no pool/listen/network/lifecycle metadata and a
// three-field unique key that cannot hold two generations of one owner.
const legacyProxyPathPortAllocationSchema = `create table proxy_path_port_allocations (id integer primary key autoincrement, kind text not null, scope_key text not null, server_id integer not null references servers(id) on delete cascade, port integer not null, created_at text not null, updated_at text not null, unique(kind,scope_key,server_id))`
