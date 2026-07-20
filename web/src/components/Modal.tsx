import { X } from "lucide-react";
import type { ReactNode } from "react";
import { IconButton } from "./IconButton";
import { useI18n } from "../i18n";

interface ModalProps {
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  onClose: () => void;
}

export function Modal({ title, children, footer, wide = false, onClose }: ModalProps) {
  const { tx } = useI18n();
  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className={`modal ${wide ? "modal-wide" : ""}`} role="dialog" aria-modal="true" aria-label={title}>
        <header className="modal-header">
          <h2>{title}</h2>
          <IconButton label={tx("ui.close")} onClick={onClose}><X size={17} /></IconButton>
        </header>
        <div className="modal-body">{children}</div>
        {footer ? <footer className="modal-footer">{footer}</footer> : null}
      </section>
    </div>
  );
}
