import { useState } from "react";
import { ExternalLink, MoreHorizontal, Pencil, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { Post, StreamItem, TimelineItem } from "@/api";
import { useAutoMarkRead } from "@/hooks/useAutoMarkRead";
import { extractLead } from "@/lib/lead";
import { relativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";

interface FeedHandlers {
  onMarkRead: (it: TimelineItem) => void;
  onMarkUnread: (it: TimelineItem) => void;
}

interface OwnHandlers {
  onEditOwn: (p: Post) => void;
  onDeleteOwn: (p: Post) => void;
}

type Props = { item: StreamItem } & FeedHandlers & OwnHandlers;

export function StreamCard(props: Props) {
  if (props.item.kind === "feed") {
    return (
      <FeedCard
        item={props.item.item}
        onMarkRead={props.onMarkRead}
        onMarkUnread={props.onMarkUnread}
      />
    );
  }
  return (
    <OwnCard
      post={props.item.post}
      onEditOwn={props.onEditOwn}
      onDeleteOwn={props.onDeleteOwn}
    />
  );
}

function FeedCard({
  item: feed,
  onMarkRead,
  onMarkUnread,
}: { item: TimelineItem } & FeedHandlers) {
  const [expanded, setExpanded] = useState(false);
  const { leadHTML, hasMore } = expanded
    ? { leadHTML: feed.content ?? "", hasMore: false }
    : extractLead(feed.content ?? "");

  const ref = useAutoMarkRead({
    enabled: !feed.read,
    onRead: () => onMarkRead(feed),
  });

  return (
    <article
      ref={ref}
      className={cn(
        "border-b border-border px-1 py-4 transition-colors",
        feed.read && "text-muted-foreground",
      )}
    >
      <header className="mb-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <div className="min-w-0 truncate">
          <span className="font-medium text-foreground/80">{feed.feed_title}</span>
          {feed.author && <span> · {feed.author}</span>}
          {feed.published_at && <span> · {relativeTime(feed.published_at)}</span>}
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 text-muted-foreground"
              aria-label="More"
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {feed.url && (
              <DropdownMenuItem asChild>
                <a href={feed.url} target="_blank" rel="noopener noreferrer">
                  <ExternalLink />
                  Open original
                </a>
              </DropdownMenuItem>
            )}
            {feed.read ? (
              <DropdownMenuItem onSelect={() => onMarkUnread(feed)}>Mark unread</DropdownMenuItem>
            ) : (
              <DropdownMenuItem onSelect={() => onMarkRead(feed)}>Mark read</DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      {feed.title && (
        <h2 className="mb-1 text-base font-semibold leading-snug">
          {feed.url ? (
            <a
              href={feed.url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-foreground hover:underline"
            >
              {feed.title}
            </a>
          ) : (
            feed.title
          )}
        </h2>
      )}

      {feed.content && (
        <div
          className={cn("post-rendered text-sm", feed.read && "post-rendered-muted")}
          dangerouslySetInnerHTML={{ __html: leadHTML }}
        />
      )}

      {hasMore && !expanded && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="mt-1 text-xs font-medium text-foreground/70 hover:text-foreground hover:underline"
        >
          Read more
        </button>
      )}
    </article>
  );
}

function OwnCard({
  post,
  onEditOwn,
  onDeleteOwn,
}: { post: Post } & OwnHandlers) {
  const [expanded, setExpanded] = useState(false);
  const { leadHTML, hasMore } = expanded
    ? { leadHTML: post.html, hasMore: false }
    : extractLead(post.html);

  return (
    <article className="border-b border-border bg-accent/20 px-1 py-4">
      <header className="mb-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <div className="min-w-0 truncate">
          <span className="font-medium text-foreground/80">You</span>
          {post.date && <span> · {relativeTime(post.date)}</span>}
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 text-muted-foreground"
              aria-label="More"
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => onEditOwn(post)}>
              <Pencil />
              Edit
            </DropdownMenuItem>
            {post.path && (
              <DropdownMenuItem asChild>
                <a href={post.path} target="_blank" rel="noopener noreferrer">
                  <ExternalLink />
                  View on site
                </a>
              </DropdownMenuItem>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onSelect={() => onDeleteOwn(post)}
              className="text-destructive focus:text-destructive"
            >
              <Trash2 />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      {post.title && (
        <h2 className="mb-1 text-base font-semibold leading-snug">
          <a
            href={post.path}
            target="_blank"
            rel="noopener noreferrer"
            className="text-foreground hover:underline"
          >
            {post.title}
          </a>
        </h2>
      )}

      <div
        className="post-rendered text-sm"
        dangerouslySetInnerHTML={{ __html: leadHTML }}
      />

      {hasMore && !expanded && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="mt-1 text-xs font-medium text-foreground/70 hover:text-foreground hover:underline"
        >
          Read more
        </button>
      )}
    </article>
  );
}
