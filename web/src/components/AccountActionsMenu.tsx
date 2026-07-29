import { Ellipsis, LoaderCircle, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

interface AccountActionsMenuProps {
  label: string;
  menuLabel: string;
  refreshLabel: string;
  deleteLabel: string;
  disabled?: boolean;
  disabledReason?: string;
  refreshing?: boolean;
  onRefresh: () => void;
  onDelete: () => void;
}

interface MenuPosition {
  left: number;
  top: number;
}

export function AccountActionsMenu({
  label,
  menuLabel,
  refreshLabel,
  deleteLabel,
  disabled = false,
  disabledReason,
  refreshing = false,
  onRefresh,
  onDelete,
}: AccountActionsMenuProps) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<MenuPosition>({ left: 0, top: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    const menuWidth = 196;
    const menuHeight = 92;
    const viewportPadding = 8;
    const gap = 5;
    const left = Math.max(viewportPadding, Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - viewportPadding));
    const top = window.innerHeight - rect.bottom >= menuHeight + gap
      ? rect.bottom + gap
      : Math.max(viewportPadding, rect.top - menuHeight - gap);
    setPosition({ left, top });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const closeOnPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target && (triggerRef.current?.contains(target) || menuRef.current?.contains(target))) return;
      setOpen(false);
    };
    const closeOnKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setOpen(false);
      triggerRef.current?.focus();
    };
    const closeOnViewportChange = () => setOpen(false);
    document.addEventListener("pointerdown", closeOnPointerDown);
    document.addEventListener("keydown", closeOnKeyDown);
    window.addEventListener("resize", closeOnViewportChange);
    window.addEventListener("scroll", closeOnViewportChange, true);
    return () => {
      document.removeEventListener("pointerdown", closeOnPointerDown);
      document.removeEventListener("keydown", closeOnKeyDown);
      window.removeEventListener("resize", closeOnViewportChange);
      window.removeEventListener("scroll", closeOnViewportChange, true);
    };
  }, [open]);

  const run = (action: () => void) => {
    setOpen(false);
    action();
  };

  return (
    <>
      <button
        ref={triggerRef}
        className="icon-button row-more-action"
        type="button"
        aria-label={disabled ? disabledReason || label : label}
        title={disabled ? disabledReason || label : label}
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((current) => !current)}
      >
        {refreshing ? <LoaderCircle className="spin" size={15} /> : <Ellipsis size={16} />}
      </button>
      {open ? createPortal(
        <div
          ref={menuRef}
          className="account-actions-menu"
          role="menu"
          aria-label={menuLabel}
          style={{ left: position.left, top: position.top }}
        >
          <button type="button" role="menuitem" disabled={refreshing} onClick={() => run(onRefresh)}>
            {refreshing ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
            <span>{refreshLabel}</span>
          </button>
          <button className="danger" type="button" role="menuitem" disabled={refreshing} onClick={() => run(onDelete)}>
            <Trash2 size={15} />
            <span>{deleteLabel}</span>
          </button>
        </div>,
        document.body,
      ) : null}
    </>
  );
}
