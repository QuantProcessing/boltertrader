import { RefreshCw } from "lucide-react";
import { useState } from "react";

import type { ActivityEvent, AuditRecord } from "../types";
import { activityDisplay } from "../view-models";
import { StatusBadge } from "../../components/StatusBadge";

interface ActivityProps { activity: ActivityEvent[]; audit: AuditRecord[]; onRefresh: () => void }

export function ActivityRoute({ activity, audit, onRefresh }: ActivityProps) {
  const [tab, setTab] = useState<"activity" | "audit">("activity");
  return <div className="route-stack">
    <header className="page-heading"><div><p className="eyebrow">Platform / Evidence</p><h1>Activity & Audit</h1></div><button className="button button--secondary" onClick={onRefresh}><RefreshCw />Refresh</button></header>
    <div className="tabs" role="tablist" aria-label="Evidence views"><button role="tab" aria-selected={tab === "activity"} className={tab === "activity" ? "is-active" : ""} onClick={() => setTab("activity")}>Activity</button><button role="tab" aria-selected={tab === "audit"} className={tab === "audit" ? "is-active" : ""} onClick={() => setTab("audit")}>Audit</button></div>
    {tab === "activity" ? <section className="surface surface--table" role="tabpanel"><div className="section-heading"><div><h2>Operational activity</h2><span>{activity.length} events</span></div></div><div className="table-scroll"><table><thead><tr><th>Time</th><th>Kind</th><th>Summary</th><th>Status</th><th>Resource</th></tr></thead><tbody>{activity.map((item) => {
        const display = activityDisplay(item);
        return <tr key={item.eventId}><td>{item.observedAt}</td><td>{item.kind}</td><td>{display.summary}</td><td><StatusBadge state={display.status} /></td><td><code>{item.resourceId ?? "-"}</code></td></tr>;
      })}</tbody></table></div>{activity.length === 0 && <div className="empty-row">No operational activity.</div>}</section>
      : <section className="surface surface--table" role="tabpanel"><div className="section-heading"><div><h2>Audit log</h2><span>{audit.length} immutable records</span></div></div><div className="table-scroll"><table><thead><tr><th>Time</th><th>Action</th><th>Resource</th><th>Outcome</th><th>Request</th></tr></thead><tbody>{audit.map((item) => <tr key={item.auditId}><td>{item.occurredAt}</td><td>{item.action}</td><td>{item.resourceType} · <code>{item.resourceId}</code></td><td><StatusBadge state={item.outcome} /></td><td><code>{item.requestId}</code></td></tr>)}</tbody></table></div>{audit.length === 0 && <div className="empty-row">No audit records.</div>}</section>}
  </div>;
}
