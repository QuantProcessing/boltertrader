alter table ledger_closed_position_projections
  drop constraint ledger_closed_position_projections_pkey;

alter table ledger_closed_position_projections
  add primary key (principal_id, mode, reporting_asset, position_id);
