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
  // Default to JSON for plain string bodies. For FormData, leave the
  // header unset so the browser can add the multipart boundary.
  const isForm = init.body instanceof FormData;
  const r = await fetch(path, {
    ...init,
    headers: {
      ...(init.body && !isForm ? { "content-type": "application/json" } : {}),
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
  html: string;
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

export type Media = {
  name: string;
  url: string;
  size: number;
  mime: string;
};

export async function uploadMedia(file: File): Promise<Media> {
  const fd = new FormData();
  fd.append("file", file);
  return api<Media>("/admin/api/media", { method: "POST", body: fd });
}

export async function updatePost(id: string, body: { title: string; body: string; tags?: string[] }): Promise<Post> {
  return api<Post>(`/admin/api/posts/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export async function deletePost(id: string): Promise<void> {
  await api<void>(`/admin/api/posts/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export type Draft = {
  id: string;
  title?: string;
  tags?: string[];
  body: string;
  html: string;
  created: string;
};

export async function listDrafts(): Promise<Draft[]> {
  return api<Draft[]>("/admin/api/drafts");
}

export async function createDraft(body: { title: string; body: string; tags?: string[] }): Promise<Draft> {
  return api<Draft>("/admin/api/drafts", { method: "POST", body: JSON.stringify(body) });
}

export async function updateDraft(id: string, body: { title: string; body: string; tags?: string[] }): Promise<Draft> {
  return api<Draft>(`/admin/api/drafts/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export async function deleteDraft(id: string): Promise<void> {
  await api<void>(`/admin/api/drafts/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function publishDraft(id: string): Promise<Post> {
  return api<Post>(`/admin/api/drafts/${encodeURIComponent(id)}/publish`, { method: "POST" });
}
