import { useState } from "react";
import { PageHeader, Tabs } from "../../ui/kit";
import { useCopy } from "../shared";
import { InspectionWorkspace } from "../../components/InspectionWorkspace";
import { AutomationPolicySettings } from "../../components/AutomationPolicySettings";
import { ExternalNotificationSettings } from "../../components/ExternalNotificationSettings";
import { AutoRetryCard } from "../../components/AutoRetryCard";
import { ProxyProfilesSettings } from "../../components/ProxyProfilesSettings";

// The automation surfaces keep their tested implementations; the new shell owns
// navigation so they stop being four top-level pages.
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
  const copy = useCopy();
  const [tab, setTab] = useState<AutomationTab>("inspection");
  return (
    <div className="space-y-4">
      <PageHeader title={copy.automation.title} subtitle={copy.automation.subtitle} />
      <Tabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "inspection", label: copy.automation.inspection },
          { id: "policies", label: copy.automation.policies },
          { id: "notifications", label: copy.automation.notifications },
          { id: "retry", label: copy.automation.retry },
          { id: "proxy", label: copy.automation.proxy },
        ]}
      />
      <div className="rounded-xl border border-gray-200 bg-white p-4">
        {tab === "inspection" ? <InspectionWorkspace onAPIError={onAPIError} onNotice={onNotice} /> : null}
        {tab === "policies" ? (
          <AutomationPolicySettings
            refreshRevision={refreshRevision}
            forceLoading={false}
            onAPIError={onAPIError}
            onNotice={onNotice}
            onForcePreview={() => onNotice(copy.automation.policies)}
          />
        ) : null}
        {tab === "notifications" ? (
          <ExternalNotificationSettings refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />
        ) : null}
        {tab === "retry" ? <AutoRetryCard refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} /> : null}
        {tab === "proxy" ? <ProxyProfilesSettings refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} /> : null}
      </div>
    </div>
  );
}
