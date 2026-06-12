alter table users
  add column if not exists expiry_synced_at timestamptz;
