import { useState } from "react";
import { PageHeader, Tabs } from "../../ui/kit";
import { ExperimentsTab, GeneralTab, OperationsTab, PluginsTab, RiskTab, UpdatesTab } from "./settings/tabs";
import { useSettingsCopy } from "./settings/copy";
import { useCopy } from "../shared";

// The settings page is a navigation shell over six rebuilt tabs. Destructive
// actions inside the tabs always confirm first, and a failed call is shown inline
// instead of being swallowed by the shell.
type SettingsTab = "general" | "operations" | "risk" | "experiments" | "plugins" | "updates";

export function SettingsPage({
  onAPIError,
  onNotice,
}: {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const copy = useSettingsCopy();
  // The page title belongs to the page, not to whichever tab was hard-coded.
  const pageCopy = useCopy();
  const [tab, setTab] = useState<SettingsTab>("general");
  const onError = (message: string) => onAPIError(new Error(message));

  return (
    <div className="v2-page">
      <PageHeader title={pageCopy.settings.title} subtitle={pageCopy.settings.subtitle} />
      <Tabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "general", label: copy.general },
          { id: "operations", label: copy.operations },
          { id: "risk", label: copy.risk },
          { id: "experiments", label: copy.experiments },
          { id: "plugins", label: copy.plugins },
          { id: "updates", label: copy.updates },
        ]}
      />
      {tab === "general" ? <GeneralTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "operations" ? <OperationsTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "risk" ? <RiskTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "experiments" ? <ExperimentsTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "plugins" ? <PluginsTab onError={onError} onNotice={onNotice} /> : null}
      {tab === "updates" ? <UpdatesTab onError={onError} onNotice={onNotice} /> : null}
    </div>
  );
}
