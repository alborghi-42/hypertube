import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';

import type { AuthUser } from '../auth/auth.service';

/** Relative to <base href>, like the other API clients. */
const USERS_API = 'api/users';

/** Must match avatarMaxBytes in backend/internal/auth/avatar.go. */
export const AVATAR_MAX_BYTES = 5 * 1024 * 1024;
export const AVATAR_TYPES = ['image/jpeg', 'image/png', 'image/webp', 'image/gif'];

/** Another user's profile as returned by GET /api/users/:id (no email). */
export interface PublicUser {
  id: string;
  username: string;
  first_name: string;
  last_name: string;
  avatar_url: string | null;
}

export interface ProfileUpdate {
  email: string;
  username: string;
  first_name: string;
  last_name: string;
  language: string;
}

/** Profile endpoints. HttpClient (not fetch) so the session cookie and XSRF header are sent. */
@Injectable({ providedIn: 'root' })
export class ProfileApi {
  private readonly http = inject(HttpClient);

  /** By UUID or username. */
  get(idOrUsername: string): Promise<PublicUser> {
    return firstValueFrom(
      this.http.get<PublicUser>(`${USERS_API}/${encodeURIComponent(idOrUsername)}`),
    );
  }

  update(payload: ProfileUpdate): Promise<AuthUser> {
    return firstValueFrom(this.http.patch<AuthUser>(`${USERS_API}/me`, payload));
  }

  /** Partial update: only the preferred language (the backend keeps the other fields). */
  updateLanguage(language: string): Promise<AuthUser> {
    return firstValueFrom(this.http.patch<AuthUser>(`${USERS_API}/me`, { language }));
  }

  uploadAvatar(file: File): Promise<AuthUser> {
    const body = new FormData();
    body.append('avatar', file);
    return firstValueFrom(this.http.post<AuthUser>(`${USERS_API}/me/avatar`, body));
  }
}
