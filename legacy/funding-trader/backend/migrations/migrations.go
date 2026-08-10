package migrations

import _ "embed"

//go:embed 0001_init.sql
var InitSQL string

//go:embed 0002_read_model.sql
var ReadModelSQL string

//go:embed 0003_execution.sql
var ExecutionSQL string

//go:embed 0004_product_mode.sql
var ProductModeSQL string

//go:embed 20260801_ledger_facts_and_projections.sql
var LedgerFactsAndProjectionsSQL string

//go:embed 20260802_scope_closed_position_projections.sql
var ScopeClosedPositionProjectionsSQL string

//go:embed 20260802_execution_lineage_backfill.sql
var ExecutionLineageBackfillSQL string

//go:embed 20260802_execution_position_backfill.sql
var ExecutionPositionBackfillSQL string

type Migration struct {
	Version int
	SQL     string
}

func All() []Migration {
	return append(append(Base(), ProductMode()), PostCutoverMigrations()...)
}

func Base() []Migration {
	return []Migration{
		{Version: 1, SQL: InitSQL},
		{Version: 2, SQL: ReadModelSQL},
		{Version: 3, SQL: ExecutionSQL},
	}
}

func ProductMode() Migration {
	return Migration{Version: 4, SQL: ProductModeSQL}
}

func LedgerFactsAndProjections() Migration {
	return Migration{Version: 5, SQL: LedgerFactsAndProjectionsSQL}
}

func PostCutover() Migration {
	return LedgerFactsAndProjections()
}

func LedgerProjectionScopeIsolation() Migration {
	return Migration{Version: 6, SQL: ScopeClosedPositionProjectionsSQL}
}

func ExecutionLineageBackfill() Migration {
	return Migration{Version: 7, SQL: ExecutionLineageBackfillSQL}
}

func ExecutionPositionBackfill() Migration {
	return Migration{Version: 8, SQL: ExecutionPositionBackfillSQL}
}

func PostCutoverMigrations() []Migration {
	return []Migration{LedgerFactsAndProjections(), LedgerProjectionScopeIsolation(), ExecutionLineageBackfill(), ExecutionPositionBackfill()}
}
