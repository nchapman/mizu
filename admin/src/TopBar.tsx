import { LogOut, MoreHorizontal, Newspaper, Rss, Settings as SettingsIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

import type { Route } from "@/lib/router";

interface Props {
  onNavigate: (r: Route) => void;
  onLogout: () => void;
}

export function TopBar({ onNavigate, onLogout }: Props) {
  return (
    <header className="sticky top-0 z-30 mb-6 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
      <div className="mx-auto flex max-w-2xl items-center justify-between px-4 py-3">
        <button
          type="button"
          onClick={() => onNavigate("home")}
          className="text-base font-semibold tracking-tight text-foreground transition-colors hover:text-foreground/80"
        >
          repeat
        </button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label="Menu">
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            {/* Timeline lives here during the migration; step 5 folds it */}
            {/* into the unified stream on the home route. */}
            <DropdownMenuItem onSelect={() => onNavigate("timeline")}>
              <Newspaper />
              Timeline
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onNavigate("subscriptions")}>
              <Rss />
              Subscriptions
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onNavigate("settings")}>
              <SettingsIcon />
              Settings
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={onLogout}>
              <LogOut />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}
