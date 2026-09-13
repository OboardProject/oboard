create table if not exists server_delivery_flags (
		server_id integer primary key references servers(id) on delete cascade,
		authorization_fast_lane integer not null default 1,
		runtime_users_enabled integer not null default 1
	);
