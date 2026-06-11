alter table users
  add column if not exists quota_mb integer not null default 0;
