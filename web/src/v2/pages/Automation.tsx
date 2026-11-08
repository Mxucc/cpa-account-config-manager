import { useState } from "react";
import { PageHeader, Tabs } from "../../ui/kit";
import { InspectionTab, NotificationsTab, PoliciesTab, ProxyTab, RetryTab } from "./automation/tabs";
import { useAutomationCopy } from "./automation/copy";

// The automation page is a navigation shell over five rebuilt tabs. Each tab owns
// its own data, its own loading and error states, and its own polling, so a slow
// surface never blocks the others.
type AutomationTab = "inspection" | "policies" | "notifications" | "retry" | "proxy";

export function AutomationPage({
  refreshRevision,
  onAPIError,
  onNotice,
}: {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const copy = useAutomationCopy();
  const [tab, setTab] = useState<AutomationTab>("inspection");
  // A 401 has to reach the shell so it can clear the session; every other failure
  // is the tab's own business and is shown inline.
  const onError = (message: string) => onAPIError(new Error(message));

  return (
    <div className="space-y-4">
      <PageHeader title={copy.inspection} subtitle={copy.policies} />
      <Tabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "inspection", label: copy.inspection },
          { id: "policies", label: copy.policies },
          { id: "notifications", label: copy.notifications },
          { id: "retry", label: copy.retry },
          { id: "proxy", label: copy.proxy },
        ]}
      />
      {tab === "inspection" ? (
        <InspectionTab refreshRevision={refreshRevision} onError={onError} onNotice={onNotice} />
      ) : null}
      {tab === "policies" ? <PoliciesTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "notifications" ? <NotificationsTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "retry" ? <RetryTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "proxy" ? <ProxyTab onError={onError} onNotice={onNotice} /> : null}
    </div>
  );
}
