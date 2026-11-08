import { useMemo } from "react";
import { ClinePassWorkspace } from "../../components/ClinePassWorkspace";
import { CodexWorkspace } from "../../components/CodexWorkspace";
import { OpenCodeWorkspace } from "../../components/OpenCodeWorkspace";

// LegacyConsole is the compatibility layer, kept in its own module on purpose.
//
// The three workspaces below still own deep provider controls that the rebuilt
// pages do not replace yet (Codex identity policy and fingerprint, OpenCode
// session routing and model control, Cline Pass credential repair and channel
// binding). They are embedded unchanged inside the compatibility tab, and they
// are the only place in the v2 tree that reaches into the old components — one
// named file instead of imports scattered through the new pages. When a
// capability moves into the v2 pages, remove it here.
export function LegacyConsole({
  providerID,
  refreshRevision,
  onAPIError,
  onNotice,
}: {
  providerID?: string;
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const workspace = useMemo(() => {
    switch (providerID) {
      case "codex":
        return <CodexWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      case "opencode-go":
      case "opencode-zen":
        return <OpenCodeWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      case "cline-pass":
        return <ClinePassWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      default:
        return null;
    }
  }, [providerID, refreshRevision, onAPIError, onNotice]);
  if (!workspace) return null;
  // The old stylesheet owns these workspaces; the marker class keeps the v2
  // button reset and the v2 layout rules away from them.
  return <div className="legacy-console rounded-xl border border-gray-200 bg-white p-4">{workspace}</div>;
}
