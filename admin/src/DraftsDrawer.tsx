import { DraftsView } from "@/Drafts";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { Draft } from "@/api";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAuthLost: () => void;
  onEdit: (d: Draft) => void;
  // Bumped from the parent when the drafts list might have changed
  // (composer saved a draft, etc.) so the inner list refetches when shown.
  refreshKey?: number;
  onChanged?: () => void;
}

export function DraftsDrawer({
  open,
  onOpenChange,
  onAuthLost,
  onEdit,
  refreshKey = 0,
  onChanged,
}: Props) {
  function handleEdit(d: Draft) {
    onEdit(d);
    onOpenChange(false);
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader className="mb-4">
          <SheetTitle>Drafts</SheetTitle>
          <SheetDescription>Continue something you started.</SheetDescription>
        </SheetHeader>
        <DraftsView
          onAuthLost={onAuthLost}
          onEdit={handleEdit}
          refreshKey={refreshKey}
          onChanged={onChanged}
        />
      </SheetContent>
    </Sheet>
  );
}
