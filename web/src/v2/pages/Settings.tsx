import { useState } from "react";
import { PageHeader, Tabs } from "../../ui/kit";
import { useCopy } from "../shared";
import { OtherSettingsWorkspace } from "../../components/OtherSettingsWorkspace";
import { OperationLogWorkspace } from "../../components/OperationLogWorkspace";
import { RiskControlWorkspace } from "../../components/RiskControlWorkspace";
import { SelfUpdatePanel } from "../../components/SelfUpdatePanel";

type SettingsTab = "general" | "operations" | "risk" | "update";

export function SettingsPage({
  onAPIError,
  onNotice,
}: {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const copy = useCopy();
  const [tab, setTab] = useState<SettingsTab>("general");
  return (
    <div className="space-y-4">
      <PageHeader title={copy.settings.title} subtitle={copy.settings.subtitle} />
      <Tabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "general", label: copy.settings.general },
          { id: "operations", label: copy.settings.operations },
          { id: "risk", label: copy.settings.risk },
          { id: "update", label: copy.settings.update },
        ]}
      />
      <div className="rounded-xl border border-gray-200 bg-white p-4">
        {tab === "general" ? <OtherSettingsWorkspace onAPIError={onAPIError} onNotice={onNotice} /> : null}
        {tab === "operations" ? (
          <OperationLogWorkspace
            activeJobIDs={[]}
            onAPIError={onAPIError}
            onNotice={onNotice}
            onOpenRelatedJob={() => undefined}
          />
        ) : null}
        {tab === "risk" ? <RiskControlWorkspace onAPIError={onAPIError} onNotice={onNotice} /> : null}
        {tab === "update" ? <SelfUpdatePanel onAPIError={onAPIError} onNotice={onNotice} /> : null}
      </div>
    </div>
  );
}
