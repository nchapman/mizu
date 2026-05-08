// Thin fetch wrapper that surfaces 401 to the caller so the app can
// drop back to the login screen, and otherwise throws on non-2xx with
// the response body as the error message.

export class Unauthorized extends Error {
  constructor() {
    super("unauthorized");
  }
}

export async function api<T = unknown>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const r = await fetch(path, {
    ...init,
    headers: {
      ...(init.body ? { "content-type": "application/json" } : {}),
      ...(init.headers || {}),
    },
  });
  if (r.status === 401) throw new Unauthorized();
  if (!r.ok) throw new Error((await r.text()) || `HTTP ${r.status}`);
  if (r.status === 204) return undefined as T;
  return r.json() as Promise<T>;
}

export type Post = {
  id: string;
  title?: string;
  date: string;
  tags?: string[];
  body: string;
  path: string;
};

export type Subscription = {
  id: number;
  url: string;
  title: string;
  site_url?: string;
  category?: string;
  last_fetched_at?: string;
  last_error?: string;
};

export type TimelineItem = {
  id: number;
  feed_id: number;
  feed_title: string;
  url?: string;
  title?: string;
  author?: string;
  content?: string;
  published_at?: string;
  read: boolean;
};

export type Timeline = {
  items: TimelineItem[];
  next_cursor?: string;
};
