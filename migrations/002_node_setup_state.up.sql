alter table nodes
  add column if not exists setup_state text not null default 'planned'
  check (setup_state in ('planned', 'connected', 'unreachable', 'disabled'));
