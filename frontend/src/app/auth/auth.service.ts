import { HttpClient } from '@angular/common/http';
import { Injectable, computed, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';

/** Relative to <base href>, like the media API, so it follows the deploy prefix. */
const AUTH_API = 'api/auth';

/** The logged-in user as returned by GET /api/auth/me (email is only ever sent to its owner). */
export interface AuthUser {
  id: string;
  username: string;
  email: string;
  first_name: string;
  last_name: string;
  avatar_url: string | null;
  language: string;
}

export interface RegisterPayload {
  email: string;
  username: string;
  first_name: string;
  last_name: string;
  password: string;
}

/** Error body of the auth endpoints: `fields` maps form control names to messages. */
export interface AuthErrorBody {
  error?: string;
  fields?: Record<string, string>;
}

/**
 * Session state. The session itself lives in an httpOnly cookie set by the backend;
 * this service only mirrors who is logged in. `undefined` means "not checked yet".
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly http = inject(HttpClient);
  private readonly state = signal<AuthUser | null | undefined>(undefined);
  private pending: Promise<AuthUser | null> | null = null;

  readonly user = computed(() => this.state() ?? null);
  readonly isLoggedIn = computed(() => this.user() !== null);

  /** Resolves the current user (cached after the first call); null when logged out. */
  loadUser(): Promise<AuthUser | null> {
    const known = this.state();
    if (known !== undefined) {
      return Promise.resolve(known);
    }
    this.pending ??= firstValueFrom(this.http.get<AuthUser>(`${AUTH_API}/me`))
      .then(
        (user) => {
          this.state.set(user);
          return user;
        },
        () => {
          this.state.set(null);
          return null;
        },
      )
      .finally(() => {
        this.pending = null;
      });
    return this.pending;
  }

  async login(username: string, password: string): Promise<AuthUser> {
    const user = await firstValueFrom(
      this.http.post<AuthUser>(`${AUTH_API}/login`, { username, password }),
    );
    this.state.set(user);
    return user;
  }

  async register(payload: RegisterPayload): Promise<AuthUser> {
    const user = await firstValueFrom(this.http.post<AuthUser>(`${AUTH_API}/register`, payload));
    this.state.set(user);
    return user;
  }

  async logout(): Promise<void> {
    try {
      await firstValueFrom(this.http.post<void>(`${AUTH_API}/logout`, null));
    } finally {
      this.state.set(null);
    }
  }

  /** Called by the interceptor when the backend answers 401 (session expired/revoked). */
  markLoggedOut(): void {
    this.state.set(null);
  }
}

/** Only allow in-app absolute paths as post-login redirects (no `//host` or schemes). */
export function safeReturnUrl(value: string | null | undefined): string {
  return value !== null && value !== undefined && /^\/(?![/\\])/.test(value) ? value : '/';
}
