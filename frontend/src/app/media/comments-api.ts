import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';

/** Must match MaxCommentLength in backend/internal/media/models.go. */
export const MAX_COMMENT_LENGTH = 1000;

export interface MediaComment {
  id: string;
  media_id: string;
  user_id: string;
  username: string;
  content: string;
  created_at: string;
  updated_at: string;
}

/**
 * Comment endpoints (session-only). Uses HttpClient, not fetch, so the auth interceptor sends the
 * session cookie and the XSRF header is added to POST/PATCH/DELETE.
 */
@Injectable({ providedIn: 'root' })
export class CommentsApi {
  private readonly http = inject(HttpClient);

  list(mediaId: string): Promise<MediaComment[]> {
    return firstValueFrom(this.http.get<MediaComment[]>(commentsUrl(mediaId)));
  }

  create(mediaId: string, content: string): Promise<MediaComment> {
    return firstValueFrom(this.http.post<MediaComment>(commentsUrl(mediaId), { content }));
  }

  update(mediaId: string, commentId: string, content: string): Promise<MediaComment> {
    return firstValueFrom(
      this.http.patch<MediaComment>(commentsUrl(mediaId, commentId), { content }),
    );
  }

  remove(mediaId: string, commentId: string): Promise<void> {
    return firstValueFrom(this.http.delete<void>(commentsUrl(mediaId, commentId)));
  }
}

/** Relative to <base href>, like the rest of the API. */
function commentsUrl(mediaId: string, commentId?: string): string {
  const base = `api/media/${encodeURIComponent(mediaId)}/comments`;
  return commentId === undefined ? base : `${base}/${encodeURIComponent(commentId)}`;
}
