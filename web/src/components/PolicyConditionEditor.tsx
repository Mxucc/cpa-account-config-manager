import { GitBranch, Plus, Trash2 } from "lucide-react";
import { useI18n } from "../i18n";
import type { PolicyCondition, PolicyConditionField, PolicyConditionGroup } from "../types";
import { IconButton } from "./IconButton";

interface PolicyConditionEditorProps {
  group: PolicyConditionGroup;
  disabled: boolean;
  onChange: (group: PolicyConditionGroup) => void;
}

export function PolicyConditionEditor({ group, disabled, onChange }: PolicyConditionEditorProps) {
  return <ConditionGroupEditor group={group} depth={0} disabled={disabled} onChange={onChange} />;
}

function ConditionGroupEditor({ group, depth, disabled, onChange }: PolicyConditionEditorProps & { depth: number }) {
  const { tx } = useI18n();
  const conditions = group.conditions ?? [];
  const groups = group.groups ?? [];
  return (
    <div className={`condition-group condition-depth-${depth}`}>
      <div className="condition-group-toolbar">
        <div className="condition-operator" role="group" aria-label={tx("ui.condition_operator")}>
          <button type="button" className={group.operator === "all" ? "active" : ""} aria-pressed={group.operator === "all"} disabled={disabled} onClick={() => onChange({ ...group, operator: "all" })}>{tx("ui.match_all")}</button>
          <button type="button" className={group.operator === "any" ? "active" : ""} aria-pressed={group.operator === "any"} disabled={disabled} onClick={() => onChange({ ...group, operator: "any" })}>{tx("ui.match_any")}</button>
        </div>
        <button className="button button-quiet" type="button" disabled={disabled || conditions.length + groups.length >= 32} onClick={() => onChange({ ...group, conditions: [...conditions, { field: "provider", value: "" }] })}><Plus size={13} />{tx("ui.add_condition")}</button>
        {depth < 3 ? <button className="button button-quiet" type="button" disabled={disabled || conditions.length + groups.length >= 32} onClick={() => onChange({ ...group, groups: [...groups, { operator: "all", conditions: [{ field: "account_type", value: "" }], groups: [] }] })}><GitBranch size={13} />{tx("ui.add_condition_group")}</button> : null}
      </div>
      <div className="condition-items">
        {conditions.map((condition, index) => <ConditionRow key={`${index}:${condition.field}`} condition={condition} disabled={disabled} onChange={(next) => onChange({ ...group, conditions: conditions.map((item, itemIndex) => itemIndex === index ? next : item) })} onDelete={() => onChange({ ...group, conditions: conditions.filter((_, itemIndex) => itemIndex !== index) })} />)}
        {groups.map((child, index) => <div className="nested-condition-group" key={index}><ConditionGroupEditor group={child} depth={depth + 1} disabled={disabled} onChange={(next) => onChange({ ...group, groups: groups.map((item, itemIndex) => itemIndex === index ? next : item) })} /><IconButton label={tx("ui.delete_condition_group")} disabled={disabled} onClick={() => onChange({ ...group, groups: groups.filter((_, itemIndex) => itemIndex !== index) })}><Trash2 size={14} /></IconButton></div>)}
      </div>
    </div>
  );
}

function ConditionRow({ condition, disabled, onChange, onDelete }: { condition: PolicyCondition; disabled: boolean; onChange: (condition: PolicyCondition) => void; onDelete: () => void }) {
  const { tx } = useI18n();
  const placeholders: Record<PolicyConditionField, string> = { provider: "codex", account_type: "free", email_suffix: "example.com" };
  return <div className="condition-row"><select value={condition.field} disabled={disabled} onChange={(event) => onChange({ field: event.target.value as PolicyConditionField, value: "" })} aria-label={tx("ui.condition_field")}><option value="provider">{tx("ui.provider_type")}</option><option value="account_type">{tx("ui.account_or_plan_type")}</option><option value="email_suffix">{tx("ui.email_suffix")}</option></select><input value={condition.value} maxLength={256} disabled={disabled} placeholder={placeholders[condition.field]} onChange={(event) => onChange({ ...condition, value: event.target.value })} aria-label={tx("ui.condition_value")} /><IconButton label={tx("ui.delete_condition")} disabled={disabled} onClick={onDelete}><Trash2 size={14} /></IconButton></div>;
}
