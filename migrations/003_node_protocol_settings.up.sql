alter table nodes
  add column if not exists protocol_settings jsonb not null default '{}'::jsonb;
