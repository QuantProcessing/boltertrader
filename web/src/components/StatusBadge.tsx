import { stateTone } from "../app/view-models";

export function StatusBadge({ state, label }: { state: string; label?: string }) {
  return <span className={`status-badge status-badge--${stateTone(state)}`}>{label ?? state.replaceAll("_", " ")}</span>;
}
