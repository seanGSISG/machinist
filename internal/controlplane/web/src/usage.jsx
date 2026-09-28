import React, { useCallback, useState } from "react";
import { Card } from "@/components/ui/card";
import { PageHeading } from "@/components/ui/page-heading";
import { fetchUsage, formatUsageKey, formatUsageNumber, usageGroups, usageState } from "@/usage-state";
import { usePolling } from "@/use-polling";

export function UsagePage() {
  const [groupBy, setGroupBy] = useState("executor");
  const fetcher = useCallback(() => fetchUsage(groupBy), [groupBy]);
  const request = usePolling(fetcher, 15000);
  const view = usageState({ data: request.data, error: request.error });

  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <PageHeading title="Usage" description="Executor-reported token usage across completed runs.">
      <label className="w-full sm:w-48"><span className="field-label">Group by</span><select className="field-control" value={groupBy} onChange={(event) => setGroupBy(event.target.value)}>{usageGroups.map((group) => <option key={group.value} value={group.value}>{group.label}</option>)}</select></label>
    </PageHeading>

    {view.kind === "error" && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{view.message}</div>}
    <Card className="overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-left text-sm">
          <thead className="border-b border-border bg-muted/35 text-xs uppercase tracking-wider text-muted-foreground"><tr><th className="px-4 py-3 font-semibold">{usageGroups.find((group) => group.value === groupBy)?.label}</th><th className="px-4 py-3 text-right font-semibold">Runs</th><th className="px-4 py-3 text-right font-semibold">Input tokens</th><th className="px-4 py-3 text-right font-semibold">Output tokens</th><th className="px-4 py-3 text-right font-semibold">Cached input</th><th className="px-4 py-3 text-right font-semibold">Reasoning</th></tr></thead>
          <tbody>{view.rows.map((row) => <tr key={row.key} className="border-b border-border last:border-b-0"><th scope="row" className="px-4 py-3 font-mono text-xs font-medium">{formatUsageKey(row.key)}</th><UsageCell value={row.runs} /><UsageCell value={row.input_tokens} /><UsageCell value={row.output_tokens} /><UsageCell value={row.cached_input_tokens} /><UsageCell value={row.reasoning_tokens} /></tr>)}</tbody>
        </table>
      </div>
      {view.kind === "loading" && <p role="status" className="p-10 text-center text-sm text-muted-foreground">Loading usage…</p>}
      {view.kind === "empty" && <p className="p-10 text-center text-sm text-muted-foreground">No reported usage yet.</p>}
    </Card>
  </div>;
}

function UsageCell({ value }) {
  return <td className="px-4 py-3 text-right tabular-nums">{formatUsageNumber(value)}</td>;
}
