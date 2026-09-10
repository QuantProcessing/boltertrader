-- Older binaries could dispatch while the Trading command still said accepted.
-- Preserve that uncertainty; never classify historical accepted work as unsent.
UPDATE commands SET state='uncertain', updated_at=now() WHERE state='accepted';

-- Repair orphaned nonterminal commands without replacing an existing schedule.
INSERT INTO reconciliation_tasks(command_id,reason,next_attempt_at)
SELECT command_id, CASE WHEN state='submitted' THEN 'awaiting_terminal_evidence'
                       ELSE 'ambiguous_handoff' END, now()
FROM commands WHERE state IN ('submitted','uncertain')
ON CONFLICT (command_id) DO NOTHING;
