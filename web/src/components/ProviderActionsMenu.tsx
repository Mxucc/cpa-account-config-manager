import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Ellipsis, LoaderCircle, RotateCcw } from "lucide-react";

interface ProviderActionsMenuProps {
  label: string;
  menuLabel: string;
  resetUsageLabel: string;
  resetting?: boolean;
  onResetUsage: () => void;
}

interface MenuPosition { left: number; top: number }

export function ProviderActionsMenu({ label, menuLabel, resetUsageLabel, resetting = false, onResetUsage }: ProviderActionsMenuProps) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<MenuPosition>({ left: 0, top: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    const width = 196;
    const height = 48;
    setPosition({
      left: Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8)),
      top: window.innerHeight - rect.bottom >= height + 5 ? rect.bottom + 5 : Math.max(8, rect.top - height - 5),
    });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target && (triggerRef.current?.contains(target) || menuRef.current?.contains(target))) return;
      setOpen(false);
    };
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setOpen(false); triggerRef.current?.focus(); }
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", key);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", key); };
  }, [open]);

  return <>
    <button ref={triggerRef} className="icon-button row-more-action" type="button" aria-label={label} title={label} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      {resetting ? <LoaderCircle className="spin" size={15} /> : <Ellipsis size={16} />}
    </button>
    {open ? createPortal(
      <div ref={menuRef} className="account-actions-menu" role="menu" aria-label={menuLabel} style={{ left: position.left, top: position.top }}>
        <button type="button" role="menuitem" disabled={resetting} onClick={() => { setOpen(false); onResetUsage(); }}>
          {resetting ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}
          <span>{resetUsageLabel}</span>
        </button>
      </div>,
      document.body,
    ) : null}
  </>;
}
