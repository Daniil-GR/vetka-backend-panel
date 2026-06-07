create extension if not exists pgcrypto;

create table nodes (
  id uuid primary key default gen_random_uuid(),
  node_id text unique not null,
  name text not null,
  domain text not null,
  api_url text not null,
  protocol_type text not null check (protocol_type in ('naive', 'mieru')),
  node_secret text not null,
  enabled boolean not null default true,
  desired_config_version bigint not null default 0,
  last_applied_version bigint not null default 0,
  last_seen_at timestamptz,
  last_status text,
  last_error text,
  last_sync_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table users (
  id uuid primary key default gen_random_uuid(),
  username text unique not null,
  display_name text,
  enabled boolean not null default true,
  expires_at timestamptz,
  subscription_token text unique not null,
  notes text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table user_node_access (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  node_id uuid not null references nodes(id) on delete cascade,
  protocol_type text not null check (protocol_type in ('naive', 'mieru')),
  protocol_username text not null,
  protocol_password text not null,
  enabled boolean not null default true,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique(user_id, node_id)
);

create table node_sync_events (
  id uuid primary key default gen_random_uuid(),
  node_id uuid references nodes(id) on delete cascade,
  config_version bigint not null,
  status text not null,
  http_status int,
  request_json jsonb,
  response_json jsonb,
  error text,
  created_at timestamptz not null default now()
);

create index idx_user_node_access_node_id on user_node_access(node_id);
create index idx_user_node_access_user_id on user_node_access(user_id);
create index idx_node_sync_events_node_id_created_at on node_sync_events(node_id, created_at desc);

create or replace function touch_updated_at() returns trigger as $$
begin
  new.updated_at = now();
  return new;
end;
$$ language plpgsql;

create trigger nodes_touch_updated_at before update on nodes for each row execute function touch_updated_at();
create trigger users_touch_updated_at before update on users for each row execute function touch_updated_at();
create trigger access_touch_updated_at before update on user_node_access for each row execute function touch_updated_at();

create or replace function enforce_access_protocol() returns trigger as $$
declare node_protocol text;
begin
  select protocol_type into node_protocol from nodes where id = new.node_id;
  if node_protocol is null then
    raise exception 'node not found';
  end if;
  if new.protocol_type <> node_protocol then
    raise exception 'access protocol_type must match node protocol_type';
  end if;
  return new;
end;
$$ language plpgsql;

create trigger access_protocol_match before insert or update on user_node_access for each row execute function enforce_access_protocol();

create or replace function prevent_node_protocol_change_with_access() returns trigger as $$
begin
  if old.protocol_type <> new.protocol_type and exists(select 1 from user_node_access where node_id = old.id) then
    raise exception 'cannot change node protocol_type while user assignments exist';
  end if;
  return new;
end;
$$ language plpgsql;

create trigger node_protocol_change_guard before update on nodes for each row execute function prevent_node_protocol_change_with_access();
