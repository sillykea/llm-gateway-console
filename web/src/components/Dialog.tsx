import { useEffect, useRef, type ReactNode } from "react";

interface Props {
  title: string;
  open: boolean;
  onClose: () => void;
  children: ReactNode;
}

// A native <dialog> shown as a modal: it traps focus, closes on Escape and
// returns focus to whatever opened it, with no extra code.
export function Dialog({ title, open, onClose, children }: Props) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  return (
    <dialog ref={ref} className="dialog" onClose={onClose} aria-label={title}>
      <h2>{title}</h2>
      {children}
    </dialog>
  );
}
